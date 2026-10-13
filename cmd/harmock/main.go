// Command harmock is a mock server that answers requests with the responses
// recorded in HAR files.
//
//	harmock [-i 127.0.0.1:6060] [-host recorded=host] [-ignore-query name] [-fallback URL] file.har...
//
// Requests to http://127.0.0.1:6060/<path> get the response recorded for
// <path> on any of the recorded hosts. It listens on the same address as
// harprox by default, so a client set up to record through harprox can be
// pointed at the recording without changes. On startup it lists the routes
// it answers and the settings in effect.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/callosum-api/harhar"
	"github.com/callosum-api/harhar/internal/hostrewrite"
	"github.com/callosum-api/harhar/mock"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr, nil))
}

// options are the command's settings, from its flags.
type options struct {
	addr        string
	hosts       [][2]string // recorded, replacement
	ignoreQuery []string
	fallback    *url.URL
	quiet       bool
}

// run runs the command with args until ctx is done, and returns its exit
// code. If ready is not nil, it is called with the server's URL once it is
// listening.
func run(ctx context.Context, args []string, stderr io.Writer, ready func(baseURL string)) int {
	var opts options
	flags := flag.NewFlagSet("harmock", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.addr, "i", "127.0.0.1:6060", "listen on `host:port`")
	flags.Func("host", "point URLs naming a recorded host at another host instead of this server, given as `recorded=host` (repeatable)", func(s string) error {
		recorded, host, ok := strings.Cut(s, "=")
		if !ok || recorded == "" || host == "" {
			return errors.New("want recorded=host")
		}
		opts.hosts = append(opts.hosts, [2]string{recorded, host})
		return nil
	})
	flags.Func("ignore-query", "ignore the query parameter `name` when matching requests (repeatable)", func(s string) error {
		opts.ignoreQuery = append(opts.ignoreQuery, s)
		return nil
	})
	flags.Func("fallback", "forward requests that match no recording to `URL` instead of answering 404", func(s string) error {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("must be an absolute http or https URL")
		}
		opts.fallback = u
		return nil
	})
	flags.BoolVar(&opts.quiet, "q", false, "don't log each request")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: harmock [flags] file.har...\n\nA mock server that answers requests with the responses recorded in HAR files.\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return 2
	}
	logger := log.New(stderr, "", log.LstdFlags)

	h, err := loadHARs(flags.Args())
	if err != nil {
		logger.Print(err)
		return 1
	}
	if len(h.Log.Entries) == 0 {
		logger.Print("no entries to serve")
		return 1
	}

	ln, err := net.Listen("tcp", opts.addr)
	if err != nil {
		logger.Print(err)
		return 1
	}
	baseURL := "http://" + ln.Addr().String()
	describe(stderr, h, flags.Args(), baseURL, opts)
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); !net.ParseIP(host).IsLoopback() {
		logger.Printf("WARNING: listening on %s, so anyone who can reach it can read the recorded responses", host)
	}

	srv := &http.Server{Handler: newHandler(h, opts, logger), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	logger.Printf("Listening at %s/", baseURL)
	if ready != nil {
		ready(baseURL)
	}

	select {
	case err := <-errc:
		logger.Print(err)
		return 1
	case <-ctx.Done():
		logger.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
		return 0
	}
}

// loadHARs reads the HAR files and returns their entries as one log.
func loadHARs(filenames []string) (*harhar.HAR, error) {
	all := harhar.NewHAR("harmock", "")
	for _, name := range filenames {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var h harhar.HAR
		if err := json.Unmarshal(data, &h); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		all.Log.Entries = append(all.Log.Entries, h.Log.Entries...)
	}
	return all, nil
}

// newHandler returns the mock server's handler.
func newHandler(h *harhar.HAR, opts options, logger *log.Logger) http.Handler {
	var mockOpts []mock.Option
	for _, pair := range opts.hosts {
		mockOpts = append(mockOpts, mock.WithHost(pair[0], pair[1]))
	}
	if len(opts.ignoreQuery) > 0 {
		mockOpts = append(mockOpts, mock.WithMatcher(ignoreQueryMatcher(opts.ignoreQuery)))
	}
	m := mock.New(h, mockOpts...)

	var unmatched http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, fmt.Sprintf("harmock: no recorded response for %s %s", r.Method, r.URL.RequestURI()), http.StatusNotFound)
	})
	if opts.fallback != nil {
		target := opts.fallback
		unmatched = &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) { pr.SetURL(target) }}
	}
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if missed, ok := r.Context().Value(missKey{}).(*bool); ok {
			*missed = true
		}
		unmatched.ServeHTTP(w, r)
	}))
	if opts.quiet {
		return handler
	}
	return logRequests(handler, logger, opts.fallback != nil)
}

// missKey is the context key for a *bool set when no recording matches.
type missKey struct{}

// logRequests logs each request and how it was answered.
func logRequests(next http.Handler, logger *log.Logger, forwarding bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		missed := new(bool)
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			req := r.Method + " " + r.URL.RequestURI()
			switch p := recover(); {
			case p == http.ErrAbortHandler:
				logger.Printf("%s -> connection aborted, as the recorded request failed", req)
				panic(p)
			case p != nil:
				panic(p)
			case *missed && forwarding:
				logger.Printf("%s -> no recording, forwarded: %d", req, sw.code())
			case *missed:
				logger.Printf("%s -> no recording: %d", req, sw.code())
			default:
				logger.Printf("%s -> %d", req, sw.code())
			}
		}()
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), missKey{}, missed)))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= 200 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// Unwrap lets http.ResponseController reach the underlying ResponseWriter.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ignoreQueryMatcher returns a mock matcher that ignores the named query
// parameters in both the request and the recording.
func ignoreQueryMatcher(names []string) func(*http.Request, harhar.Entry) bool {
	return func(req *http.Request, e harhar.Entry) bool {
		u, err := url.Parse(e.Request.URL)
		if err != nil {
			return false
		}
		r := req.Clone(req.Context())
		r.URL.RawQuery = dropQuery(r.URL.RawQuery, names)
		u.RawQuery = dropQuery(u.RawQuery, names)
		e.Request.URL = u.String()
		return mock.DefaultMatcher(r, e)
	}
}

// dropQuery returns query without the named parameters.
func dropQuery(query string, names []string) string {
	if len(names) == 0 || query == "" {
		return query
	}
	q, _ := url.ParseQuery(query)
	for _, name := range names {
		q.Del(name)
	}
	return q.Encode()
}

// route is the recorded entries that one kind of request matches.
type route struct {
	method, target string // target is the path and canonical query
	hosts          []string
	statuses       []int
	bodies         map[string]bool // distinct request bodies
}

// routes groups the entries in h the way the mock matches them, in the
// order each route was first recorded.
func routes(h *harhar.HAR, ignoreQuery []string) []*route {
	var list []*route
	byKey := make(map[string]*route)
	for _, e := range h.Log.Entries {
		u, err := url.Parse(e.Request.URL)
		if err != nil {
			continue
		}
		target := u.Path
		if target == "" {
			target = "/"
		}
		if q := dropQuery(u.RawQuery, ignoreQuery); q != "" {
			// Encode sorts the parameters, as matching ignores their order.
			v, _ := url.ParseQuery(q)
			target += "?" + v.Encode()
		}
		key := e.Request.Method + " " + target
		r := byKey[key]
		if r == nil {
			r = &route{method: e.Request.Method, target: target, bodies: make(map[string]bool)}
			byKey[key] = r
			list = append(list, r)
		}
		if host := hostrewrite.NormalizeHost(u.Scheme, u.Host); !slices.Contains(r.hosts, host) {
			r.hosts = append(r.hosts, host)
		}
		r.statuses = append(r.statuses, e.Response.Status)
		r.bodies[requestBodyKey(e.Request)] = true
	}
	return list
}

func requestBodyKey(r harhar.Request) string {
	var b strings.Builder
	b.WriteString(r.PostData.Text)
	for _, p := range r.PostData.Params {
		fmt.Fprintf(&b, "\x00%s=%s;%s", p.Name, p.Value, p.FileName)
	}
	return b.String()
}

// responses summarizes the statuses a route answers with, in order.
func (r *route) responses() string {
	names := make([]string, len(r.statuses))
	for i, s := range r.statuses {
		names[i] = strconv.Itoa(s)
		if s < 100 || s > 999 {
			names[i] = "failed"
		}
	}
	var s string
	switch {
	case len(names) == 1:
		s = names[0]
	case len(slices.Compact(slices.Clone(names))) == 1:
		s = fmt.Sprintf("%s x%d", names[0], len(names))
	case len(names) > 5:
		s = strings.Join(names[:5], ", ") + fmt.Sprintf(", ... (%d in all)", len(names))
	default:
		s = strings.Join(names, ", ")
	}
	if len(r.bodies) > 1 {
		s += " (by body)"
	}
	return s
}

// describe prints the routes the server answers and its settings.
func describe(w io.Writer, h *harhar.HAR, files []string, baseURL string, opts options) {
	list := routes(h, opts.ignoreQuery)
	fmt.Fprintf(w, "Loaded %d recorded responses from %s.\n\n", len(h.Log.Entries), strings.Join(files, ", "))

	fmt.Fprintf(w, "Routes (%d):\n", len(list))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  METHOD\tPATH\tRESPONSES\tRECORDED FROM")
	for _, r := range list {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", r.method, r.target, r.responses(), strings.Join(r.hosts, ", "))
	}
	tw.Flush()

	var recorded []string
	for _, r := range list {
		for _, host := range r.hosts {
			if host != "" && !slices.Contains(recorded, host) {
				recorded = append(recorded, host)
			}
		}
	}
	slices.Sort(recorded)
	mapped := make(map[string]string)
	for _, pair := range opts.hosts {
		mapped[strings.ToLower(pair[0])] = pair[1]
	}

	fmt.Fprintf(w, "\nSettings:\n")
	setting := func(name, format string, args ...any) {
		if name != "" {
			name += ":"
		}
		fmt.Fprintf(w, "  %-11s %s\n", name, fmt.Sprintf(format, args...))
	}
	setting("listening", "%s/", baseURL)
	setting("matching", "method, path and query (in any order); headers are ignored")
	if len(opts.ignoreQuery) > 0 {
		setting("", "query parameters ignored: %s", strings.Join(opts.ignoreQuery, ", "))
	}
	setting("", "requests with a body prefer the response recorded for the same body")
	setting("sequences", "a route with several responses plays them in recorded order, then repeats the last")
	setting("hosts", "any Host header is answered; URLs in response headers and text bodies are rewritten:")
	for _, host := range recorded {
		to := "this server, as the client addressed it"
		if m, ok := mapped[host]; ok {
			to = m + " (-host)"
		}
		setting("", "  %s -> %s", host, to)
	}
	for _, pair := range opts.hosts {
		if !slices.Contains(recorded, strings.ToLower(pair[0])) {
			setting("", "  %s -> %s (-host, not in the recordings)", pair[0], pair[1])
		}
	}
	if opts.fallback != nil {
		setting("unmatched", "forwarded to %s", opts.fallback.Redacted())
	} else {
		setting("unmatched", "404 Not Found")
	}
	setting("failures", "requests recorded as failed (status 0) are answered by closing the connection")
	if opts.quiet {
		setting("logging", "off (-q)")
	} else {
		setting("logging", "each request, with the status it was answered with")
	}
	fmt.Fprintln(w)
}

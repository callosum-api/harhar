// Command harreplay sends the requests recorded in HAR files again, and
// compares each new response with the recorded one.
//
//	harreplay [-t URL] [-h headers.txt] [-headers] [-ignore-header name] [-ignore-json key] [-o replayed.har] file.har...
//
// For example, to check a new deployment, or a harmock server, against a
// recording:
//
//	harreplay -t http://127.0.0.1:6060 recorded.har
//
// Requests are sent one at a time in the order they were made, without
// following redirects. Each one prints a line saying whether the response
// matched. The exit status is 1 if any response differed or any request
// failed.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/callosum-api/harhar"
	"github.com/callosum-api/harhar/internal/headerfile"
	"github.com/callosum-api/harhar/internal/hostrewrite"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options are the command's settings, from its flags.
type options struct {
	target         *url.URL
	headers        http.Header // set on every request
	compareHeaders bool
	ignoreHeaders  map[string]bool // canonical names
	ignoreJSON     map[string]bool
	maxDiffs       int
	quiet          bool
}

// run runs the command with args and returns its exit code.
func run(args []string, stdout, stderr io.Writer) int {
	opts := options{ignoreHeaders: make(map[string]bool), ignoreJSON: make(map[string]bool)}
	flags := flag.NewFlagSet("harreplay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Func("t", "send every request to `URL` instead of its recorded host, prepending URL's path to the request path", func(s string) error {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("must be an absolute http or https URL")
		}
		opts.target = u
		return nil
	})
	headerFile := flags.String("h", "", "set the request headers in `file` (\"Name: value\" lines) on every request, such as credentials that were redacted in the recording")
	flags.BoolVar(&opts.compareHeaders, "headers", false, "also compare response headers, except Date, Server and others that change on every response")
	flags.Func("ignore-header", "don't compare the response header `name` (repeatable; implies -headers)", func(s string) error {
		opts.ignoreHeaders[http.CanonicalHeaderKey(s)] = true
		opts.compareHeaders = true
		return nil
	})
	flags.Func("ignore-json", "don't compare JSON object members with the `key`, at any depth (repeatable)", func(s string) error {
		opts.ignoreJSON[s] = true
		return nil
	})
	flags.IntVar(&opts.maxDiffs, "max-diffs", 10, "show at most `n` differences per response")
	flags.BoolVar(&opts.quiet, "q", false, "print only responses that differ or fail")
	output := flags.String("o", "", "also record the replayed traffic to the HAR `file`")
	timeout := flags.Duration("timeout", 30*time.Second, "give up on each request after `duration`")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: harreplay [flags] file.har...\n\nSends the requests recorded in HAR files again, and compares the responses\nwith the recorded ones.\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return 2
	}
	if *headerFile != "" {
		var err error
		if opts.headers, err = headerfile.Load(*headerFile); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	entries, err := loadEntries(flags.Args())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	var rec *harhar.Recorder
	var transport http.RoundTripper = http.DefaultTransport
	if *output != "" {
		rec = harhar.NewRecorder(harhar.WithHeaderRedactor(headerfile.Redactor(opts.headers)))
		transport = rec.Transport(nil)
	}
	// The jar keeps the cookies the server sets now, since recorded cookie
	// values are redacted.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Transport:     transport,
		Jar:           jar,
		Timeout:       *timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	r := newReplayer(client, entries, opts)

	var counts [numVerdicts]int
	for _, e := range entries {
		res := r.replay(e)
		counts[res.verdict]++
		if opts.quiet && res.verdict == same {
			continue
		}
		fmt.Fprintf(stdout, "%-4s  %-6s %s  %s\n", res.verdict, e.Request.Method, res.url, res.summary)
		for i, d := range res.diffs {
			if i == opts.maxDiffs {
				fmt.Fprintf(stdout, "        ... and %d more\n", len(res.diffs)-i)
				break
			}
			fmt.Fprintf(stdout, "        %s\n", d)
		}
	}
	fmt.Fprintf(stdout, "\n%d requests: %d same, %d different, %d failed, %d skipped\n",
		len(entries), counts[same], counts[different], counts[failed], counts[skipped])

	if len(r.redacted) > 0 {
		var notes []string
		for _, name := range slices.Sorted(maps.Keys(r.redacted)) {
			notes = append(notes, fmt.Sprintf("%s (%d)", name, r.redacted[name]))
		}
		fmt.Fprintf(stderr, "note: these were redacted in the recording, so they were not sent: %s. Use -h to supply real values.\n", strings.Join(notes, ", "))
	}
	code := 0
	if counts[different] > 0 || counts[failed] > 0 {
		code = 1
	}
	if rec != nil {
		size, err := rec.WriteFile(*output)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stderr, "wrote %s (%.1fkb)\n", *output, float64(size)/1024.0)
	}
	return code
}

// loadEntries reads the HAR files and returns their entries in the order
// they were started.
func loadEntries(filenames []string) ([]harhar.Entry, error) {
	var entries []harhar.Entry
	for _, name := range filenames {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var h harhar.HAR
		if err := json.Unmarshal(data, &h); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		entries = append(entries, h.Log.Entries...)
	}
	// harhar records entries as they finish, so sort them back into the
	// order the requests were made.
	slices.SortStableFunc(entries, func(a, b harhar.Entry) int {
		return a.StartedDateTime.Compare(b.StartedDateTime)
	})
	return entries, nil
}

type verdict int

const (
	same verdict = iota
	different
	failed
	skipped
	numVerdicts
)

func (v verdict) String() string {
	return [...]string{"ok", "DIFF", "FAIL", "SKIP"}[v]
}

type result struct {
	verdict verdict
	url     string // as requested
	summary string
	diffs   []string
}

// replayer sends recorded requests and compares their responses.
type replayer struct {
	client *http.Client
	opts   options
	urls   *hostrewrite.Rewriter
	to     map[string]hostrewrite.Target // with -t, every recorded host -> target

	redacted map[string]int // header name -> requests it was left out of
}

func newReplayer(client *http.Client, entries []harhar.Entry, opts options) *replayer {
	r := &replayer{client: client, opts: opts, redacted: make(map[string]int)}
	if opts.target == nil {
		return r
	}
	hosts := make(map[string]bool)
	for _, e := range entries {
		if u, err := url.Parse(e.Request.URL); err == nil && u.Host != "" {
			hosts[hostrewrite.NormalizeHost(u.Scheme, u.Host)] = true
		}
	}
	r.urls = hostrewrite.New(slices.Collect(maps.Keys(hosts)))
	r.to = make(map[string]hostrewrite.Target)
	for h := range hosts {
		r.to[h] = hostrewrite.Target{Scheme: opts.target.Scheme, Host: opts.target.Host}
	}
	return r
}

// errTruncated means a recorded request body is incomplete.
var errTruncated = errors.New("the request body was not fully recorded")

func (r *replayer) replay(e harhar.Entry) result {
	req, err := r.newRequest(e)
	if err != nil {
		return result{verdict: skipped, url: e.Request.URL, summary: err.Error()}
	}
	res := result{url: req.URL.Redacted()}

	resp, err := r.client.Do(req)
	if err != nil {
		if !hasResponse(e) {
			res.summary = "failed, as recorded"
			return res
		}
		res.verdict, res.summary = failed, err.Error()
		return res
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		res.verdict, res.summary = failed, "reading the response: "+err.Error()
		return res
	}

	res.diffs = r.compare(e, req.Method, resp, body)
	res.summary = strconv.Itoa(resp.StatusCode)
	switch {
	case !hasResponse(e):
		res.summary = "failed -> " + res.summary
	case e.Response.Status != resp.StatusCode:
		res.summary = strconv.Itoa(e.Response.Status) + " -> " + res.summary
	}
	if len(res.diffs) > 0 {
		res.verdict = different
	}
	return res
}

// hasResponse reports whether e recorded a response, as opposed to a
// request that failed.
func hasResponse(e harhar.Entry) bool {
	return e.Response.Status >= 100 && e.Response.Status <= 999
}

// skipRequestHeaders are recorded headers that the client sets itself.
var skipRequestHeaders = map[string]bool{
	"Accept-Encoding":   true, // so the transport can decompress for comparison
	"Connection":        true,
	"Content-Length":    true,
	"Host":              true,
	"Keep-Alive":        true,
	"Proxy-Connection":  true,
	"Te":                true,
	"Trailer":           true,
	"Transfer-Encoding": true,
	"Upgrade":           true,
}

// newRequest rebuilds the request recorded in e.
func (r *replayer) newRequest(e harhar.Entry) (*http.Request, error) {
	u, err := url.Parse(e.Request.URL)
	if err != nil {
		return nil, err
	}
	u.Fragment, u.RawFragment = "", ""
	if _, ok := u.User.Password(); ok {
		// Recorded URLs have their passwords redacted.
		u.User = nil
		r.redacted["URL password"]++
	}
	if t := r.opts.target; t != nil {
		path := strings.TrimSuffix(t.EscapedPath(), "/") + u.EscapedPath()
		u.Scheme, u.Host, u.User = t.Scheme, t.Host, t.User
		if u.Path, err = url.PathUnescape(path); err != nil {
			return nil, err
		}
		u.RawPath = path
	}

	body, contentType, err := requestBody(e.Request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(e.Request.Method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	left := make(map[string]bool) // redacted headers not sent
	for _, h := range e.Request.Headers {
		name := http.CanonicalHeaderKey(h.Name)
		if strings.HasPrefix(name, ":") || skipRequestHeaders[name] {
			continue // HTTP/2 pseudo-headers appear in browser HARs
		}
		if _, ok := r.opts.headers[name]; ok {
			continue
		}
		if strings.Contains(h.Value, harhar.RedactedValue) {
			left[name] = true
			continue
		}
		req.Header.Add(name, r.urls.Rewrite(h.Value, r.to))
	}
	for name := range left {
		r.redacted[name]++
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, vals := range r.opts.headers {
		req.Header[name] = vals
	}
	return req, nil
}

// requestBody returns the body recorded in r, and the Content-Type to
// send it with if it isn't the recorded one.
func requestBody(r harhar.Request) ([]byte, string, error) {
	pd := r.PostData
	if len(pd.Params) == 0 {
		if r.BodySize > int64(len(pd.Text)) {
			return nil, "", errTruncated
		}
		return []byte(pd.Text), "", nil
	}

	mediaType, params, _ := mime.ParseMediaType(pd.MIMEType)
	if mediaType != "multipart/form-data" {
		pairs := make([]string, len(pd.Params))
		for i, p := range pd.Params {
			pairs[i] = url.QueryEscape(p.Name) + "=" + url.QueryEscape(p.Value)
		}
		return []byte(strings.Join(pairs, "&")), "", nil
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if b := params["boundary"]; b != "" {
		mw.SetBoundary(b) // keep the recorded boundary if it's valid
	}
	quote := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace
	for _, p := range pd.Params {
		h := make(textproto.MIMEHeader)
		disposition := `form-data; name="` + quote(p.Name) + `"`
		if p.FileName != "" {
			disposition += `; filename="` + quote(p.FileName) + `"`
		}
		h.Set("Content-Disposition", disposition)
		if p.ContentType != "" {
			h.Set("Content-Type", p.ContentType)
		}
		w, err := mw.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		io.WriteString(w, p.Value)
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

// volatileHeaders change from one response to the next, so they are not
// compared.
var volatileHeaders = map[string]bool{
	"Age":               true,
	"Alt-Svc":           true,
	"Cf-Ray":            true,
	"Connection":        true,
	"Content-Length":    true,
	"Date":              true,
	"Etag":              true,
	"Expires":           true,
	"Keep-Alive":        true,
	"Last-Modified":     true,
	"Nel":               true,
	"Report-To":         true,
	"Server":            true,
	"Server-Timing":     true,
	"Transfer-Encoding": true,
	"Via":               true,
	"X-Amz-Request-Id":  true,
	"X-Amzn-Requestid":  true,
	"X-Cache":           true,
	"X-Request-Id":      true,
	"X-Runtime":         true,
}

// compare returns the differences between the response recorded in e and
// the new one, described one per line.
func (r *replayer) compare(e harhar.Entry, method string, resp *http.Response, body []byte) []string {
	if !hasResponse(e) {
		return []string{"status: failed when recorded (" + e.Response.Comment + "), now " + resp.Status}
	}
	recorded := make(http.Header)
	for _, h := range e.Response.Headers {
		value := r.urls.Rewrite(h.Value, r.to)
		if http.CanonicalHeaderKey(h.Name) == "Set-Cookie" {
			value = hostrewrite.DropCookieDomain(value, r.to)
		}
		recorded.Add(h.Name, value)
	}

	var diffs []string
	if e.Response.Status != resp.StatusCode {
		diffs = append(diffs, fmt.Sprintf("status: %d -> %d", e.Response.Status, resp.StatusCode))
	}
	if want, got := recorded.Get("Content-Type"), resp.Header.Get("Content-Type"); want != "" && want != got {
		diffs = append(diffs, fmt.Sprintf("header Content-Type: %q -> %q", want, got))
	}
	if r.opts.compareHeaders {
		diffs = append(diffs, r.headerDiffs(recorded, resp.Header)...)
	}
	if method != http.MethodHead {
		diffs = append(diffs, r.bodyDiffs(e.Response.Content, recorded, body)...)
	}
	return diffs
}

func (r *replayer) headerDiffs(recorded, got http.Header) []string {
	names := make(map[string]bool)
	for name := range recorded {
		names[name] = true
	}
	for name := range got {
		names[name] = true
	}
	var diffs []string
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if name == "Content-Type" || volatileHeaders[name] || r.opts.ignoreHeaders[name] {
			continue
		}
		if slices.ContainsFunc(recorded[name], func(v string) bool { return strings.Contains(v, harhar.RedactedValue) }) {
			continue
		}
		was, now := strings.Join(recorded[name], ", "), strings.Join(got[name], ", ")
		switch {
		case was == now:
		case len(recorded[name]) == 0:
			diffs = append(diffs, fmt.Sprintf("header %s: added %q", name, now))
		case len(got[name]) == 0:
			diffs = append(diffs, fmt.Sprintf("header %s: removed (was %q)", name, was))
		default:
			diffs = append(diffs, fmt.Sprintf("header %s: %q -> %q", name, was, now))
		}
	}
	return diffs
}

// bodyDiffs compares the recorded body c with the new body got.
func (r *replayer) bodyDiffs(c harhar.Content, header http.Header, got []byte) []string {
	if c.Text == "" && c.Size > 0 {
		return nil // the body wasn't recorded
	}
	want := []byte(c.Text)
	isText := true
	if c.Encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(c.Text)
		if err != nil {
			return []string{"body: the recorded body is not valid base64"}
		}
		want, isText = b, false
	}
	if int64(len(want)) < c.Size {
		// Only the start of the body was recorded.
		if strings.EqualFold(header.Get("Content-Encoding"), "gzip") {
			return nil
		}
		if isText {
			want = []byte(r.urls.Rewrite(string(want), r.to))
		}
		if bytes.HasPrefix(got, want) {
			return nil
		}
		return append(textDiff(want, got[:min(len(got), len(want))]),
			fmt.Sprintf("body: differs within the first %d bytes, which is all that was recorded", len(want)))
	}
	if strings.EqualFold(header.Get("Content-Encoding"), "gzip") && !isText {
		// Recorded compressed; the new body was decompressed by the transport.
		if zr, err := gzip.NewReader(bytes.NewReader(want)); err == nil {
			if b, err := io.ReadAll(zr); err == nil {
				want, isText = b, utf8.Valid(b)
			}
		}
	}
	if isText {
		want = []byte(r.urls.Rewrite(string(want), r.to))
	}
	if bytes.Equal(want, got) && len(r.opts.ignoreJSON) == 0 {
		return nil
	}

	if a, ok := parseJSON(want); ok {
		if b, ok := parseJSON(got); ok {
			var diffs []string
			r.jsonDiff("$", a, b, &diffs)
			return diffs
		}
	}
	if bytes.Equal(want, got) {
		return nil
	}
	if utf8.Valid(want) && utf8.Valid(got) {
		return textDiff(want, got)
	}
	if len(want) != len(got) {
		return []string{fmt.Sprintf("body: %d bytes -> %d bytes", len(want), len(got))}
	}
	return []string{fmt.Sprintf("body: the %d bytes differ", len(want))}
}

// parseJSON parses data as a single JSON value, keeping numbers exact.
func parseJSON(data []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false // more than one value
	}
	return v, true
}

// jsonDiff appends the differences between JSON values a and b at path.
func (r *replayer) jsonDiff(path string, a, b any, diffs *[]string) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			break
		}
		keys := make(map[string]bool)
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			if r.opts.ignoreJSON[k] {
				continue
			}
			x, inA := av[k]
			y, inB := bv[k]
			p := path + jsonKey(k)
			switch {
			case !inB:
				*diffs = append(*diffs, fmt.Sprintf("body %s: removed (was %s)", p, jsonText(x)))
			case !inA:
				*diffs = append(*diffs, fmt.Sprintf("body %s: added %s", p, jsonText(y)))
			default:
				r.jsonDiff(p, x, y, diffs)
			}
		}
		return
	case []any:
		bv, ok := b.([]any)
		if !ok {
			break
		}
		for i := range min(len(av), len(bv)) {
			r.jsonDiff(path+"["+strconv.Itoa(i)+"]", av[i], bv[i], diffs)
		}
		if len(av) != len(bv) {
			*diffs = append(*diffs, fmt.Sprintf("body %s: %d items -> %d items", path, len(av), len(bv)))
		}
		return
	}
	// Scalars, or values whose type changed.
	if !reflect.DeepEqual(a, b) {
		*diffs = append(*diffs, fmt.Sprintf("body %s: %s -> %s", path, jsonText(a), jsonText(b)))
	}
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func jsonKey(k string) string {
	if identifier.MatchString(k) {
		return "." + k
	}
	return "[" + strconv.Quote(k) + "]"
}

// jsonText formats v as compact JSON, shortened if long.
func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return shorten(string(b), 80)
}

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}

// textDiff describes the first difference between two texts, by line.
func textDiff(a, b []byte) []string {
	la := strings.Split(string(a), "\n")
	lb := strings.Split(string(b), "\n")
	var diffs []string
	for i := range max(len(la), len(lb)) {
		x, y := "(no line)", "(no line)"
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x == y {
			continue
		}
		if i < len(la) && i < len(lb) {
			x, y = around(x, y)
		}
		diffs = append(diffs, fmt.Sprintf("body line %d: %q -> %q", i+1, x, y))
		break
	}
	if len(la) != len(lb) {
		diffs = append(diffs, fmt.Sprintf("body: %d lines -> %d lines", len(la), len(lb)))
	}
	return diffs
}

// around shortens two differing lines to the part around where they first
// differ, so long lines such as minified documents stay readable.
func around(x, y string) (string, string) {
	const before, after = 30, 50
	if len(x) <= before+after && len(y) <= before+after {
		return x, y
	}
	i := 0
	for i < len(x) && i < len(y) && x[i] == y[i] {
		i++
	}
	start := max(i-before, 0)
	for start > 0 && !utf8.RuneStart(x[start]) {
		start--
	}
	cut := func(s string) string {
		end := min(i+after, len(s))
		for end < len(s) && !utf8.RuneStart(s[end]) {
			end++
		}
		out := s[start:end]
		if start > 0 {
			out = "..." + out
		}
		if end < len(s) {
			out += "..."
		}
		return out
	}
	return cut(x), cut(y)
}

// Command harprox is a reverse proxy that records the requests passing
// through it to a HAR file.
//
//	harprox -p https://api.example.com [-i 127.0.0.1:6060] [-o results.har] [-h headers.txt]
//
// Requests to http://127.0.0.1:6060/<path> are forwarded to
// https://api.example.com/<path>. The HAR file is saved periodically while
// there is new traffic, and once more on exit.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/callosum-api/harhar"
	"github.com/callosum-api/harhar/internal/headerfile"
)

func main() {
	headerFile := flag.String("h", "", "set the request headers in `file` (\"Name: value\" lines) on every proxied request")
	every := secondsOrDuration(5 * time.Second)
	flag.Var(&every, "n", "save the HAR file every `interval` while there is new traffic, e.g. 30s (a bare number means seconds)")
	addr := flag.String("i", "127.0.0.1:6060", "listen on `host:port`")
	prefix := flag.String("p", "", "forward requests to `URL`, prepending its path to request paths (required)")
	outname := flag.String("o", "results.har", "save the HAR log to `file`")
	serverRecorder := flag.Bool("s", false, "record on the server side of the proxy instead of the client side (less timing detail)")
	maxBody := flag.Int64("max-body", harhar.DefaultMaxBodySize, "record at most `n` bytes of each body (-1 for no limit)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -p URL [flags]\n\nA reverse proxy that records requests to a HAR file.\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	target, err := url.Parse(*prefix)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		fmt.Fprintf(flag.CommandLine.Output(), "-p must be an absolute http or https URL, got %q\n\n", *prefix)
		flag.Usage()
		os.Exit(2)
	}

	var overHeaders http.Header
	if *headerFile != "" {
		overHeaders, err = headerfile.Load(*headerFile)
		if err != nil {
			log.Fatal(err)
		}
	}

	var entries atomic.Uint64
	rec := harhar.NewRecorder(
		harhar.WithHeaderRedactor(headerfile.Redactor(overHeaders)),
		harhar.WithMaxBodySize(*maxBody),
		harhar.WithOnEntry(func(harhar.Entry) { entries.Add(1) }),
	)

	proxy := newProxy(target, overHeaders)
	var handler http.Handler = proxy
	if *serverRecorder {
		handler = rec.Middleware(proxy)
	} else {
		// Recording the proxy's outbound requests captures connection timings.
		proxy.Transport = rec.Transport(nil)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); !net.ParseIP(host).IsLoopback() {
		log.Printf("WARNING: listening on %s, so anyone who can reach it can send requests through the proxy, with any headers from -h", host)
	}
	baseURL := "http://" + ln.Addr().String()
	log.Printf("Listening at %s/...", baseURL)
	log.Printf("  Requests to %s/<path> will proxy to %s/<path>", baseURL, strings.TrimSuffix(target.String(), "/"))

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	var saved uint64
	save := func() {
		n := entries.Load()
		if n == saved {
			return
		}
		size, err := rec.WriteFile(*outname)
		if err != nil {
			log.Printf("saving %s: %v", *outname, err)
			return
		}
		saved = n
		// it's always good to report size when logging since memory usage
		// will grow pretty quickly if you're not careful.
		log.Printf("[%d requests] wrote %s (%.1fkb)", n, *outname, float64(size)/1024.0)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(time.Duration(every))
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			save()
		case <-ctx.Done():
			stop() // a second interrupt exits immediately
			log.Print("shutting down")
			// Let in-flight requests finish so they are recorded.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			srv.Shutdown(shutdownCtx)
			cancel()
			save()
			return
		}
	}
}

// newProxy returns a reverse proxy to target, joining target's path with
// each request's path and setting headers on every outbound request.
func newProxy(target *url.URL, headers http.Header) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			for name, vals := range headers {
				pr.Out.Header[name] = vals
			}
		},
	}
}

// secondsOrDuration is a flag.Value for a positive duration such as "30s".
// A bare number is taken as seconds, as the flag originally was.
type secondsOrDuration time.Duration

func (d *secondsOrDuration) String() string { return time.Duration(*d).String() }

func (d *secondsOrDuration) Set(s string) error {
	v, err := time.ParseDuration(s)
	if n, nerr := strconv.Atoi(s); nerr == nil {
		v, err = time.Duration(n)*time.Second, nil
	}
	if err != nil {
		return err
	}
	if v <= 0 {
		return errors.New("must be positive")
	}
	*d = secondsOrDuration(v)
	return nil
}

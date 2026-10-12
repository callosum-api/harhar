package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/callosum-api/harhar"
)

func main() {
	headerFile := flag.String("h", "", "load request headers from `headers.txt` and overwrite on passthrough")
	rate := flag.Int("n", 5, "save HAR every `N` seconds")
	addr := flag.String("i", ":6060", "`addr:port` to listen for requests")
	prefix := flag.String("p", "", "`http://hostname/path` prefix to prepend on request paths")
	outname := flag.String("o", "results.har", "output `filename.har` to save proxied requests")
	serverRecorder := flag.Bool("s", false, "use server-side recorder for passthrough requests (less detail)")
	flag.Parse()

	var hits atomic.Uint32

	var overHeaders http.Header
	if *headerFile != "" {
		f, err := os.Open(*headerFile)
		if err != nil {
			log.Fatal(err)
		}
		overHeaders, err = parseHeaders(f)
		f.Close()
		if err != nil {
			log.Fatalf("%s: %v", *headerFile, err)
		}
	}
	rec := harhar.NewRecorder()
	hcli := http.DefaultClient

	if _, err := url.Parse(*prefix); err != nil {
		log.Fatalf("invalid -p prefix: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		defer hits.Add(1)

		newurl := *prefix + r.URL.String()
		passthrough, err := http.NewRequestWithContext(r.Context(), r.Method, newurl, r.Body)
		if err != nil {
			log.Printf("bad proxy URL %q: %v", newurl, err)
			http.Error(w, "bad request URL", http.StatusBadRequest)
			return
		}
		passthrough.ContentLength = r.ContentLength

		// The outbound Host comes from the prefix URL; r.Host is not copied.
		for h, vals := range r.Header {
			for _, val := range vals {
				passthrough.Header.Add(h, val)
			}
		}
		for h := range overHeaders {
			passthrough.Header.Set(h, overHeaders.Get(h))
		}
		resp, err := hcli.Do(passthrough)
		if err != nil {
			log.Printf("proxying %s %s: %v", r.Method, newurl, err)
			http.Error(w, "upstream request failed", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for h, vals := range resp.Header {
			for _, val := range vals {
				w.Header().Add(h, val)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	go func() {
		var lasthits uint32
		for range time.NewTicker(time.Second * time.Duration(*rate)).C {
			newhits := hits.Load()
			if newhits == lasthits {
				continue
			}
			lasthits = newhits

			size, err := rec.WriteFile(*outname)
			if err != nil {
				log.Fatal(err)
			}

			// it's always good to report size when logging since memory usage
			// will grow pretty quickly if you're not careful.
			log.Printf("[%d hits] -- wrote %s (%.1fkb)\n", newhits, *outname, float64(size)/1024.0)
		}
	}()

	srv := http.Server{Addr: *addr, Handler: nil}

	ln, err := net.Listen("tcp4", *addr)
	if err != nil {
		log.Fatal(err)
	}
	baseURL := "http://" + ln.Addr().String()
	log.Println("Listening at " + baseURL + "/...")
	log.Printf("  Requests to %s/<endpoint> will proxy to %s/<endpoint>", baseURL, *prefix)
	log.Println("")

	if *serverRecorder {
		// server-side har logging (FYI less network detail)
		srv.Handler = rec.Middleware(http.DefaultServeMux)
	} else {
		// since we're proxying every request,
		// client side works great and gets more detail
		hcli = &http.Client{Transport: rec.Transport(nil)}
	}

	err = srv.Serve(ln)
	if err != nil {
		log.Fatal(err)
	}
}

// parseHeaders reads "Name: value" lines, skipping blank lines. A repeated
// name keeps its last value.
func parseHeaders(r io.Reader) (http.Header, error) {
	h := make(http.Header)
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("line %d: want \"Name: value\", got %q", n, line)
		}
		h.Set(name, strings.TrimSpace(value))
	}
	return h, sc.Err()
}

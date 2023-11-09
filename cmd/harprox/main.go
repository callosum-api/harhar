package main

import (
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pbnjay/harhar"
)

func main() {
	headerFile := flag.String("h", "", "load request headers from `headers.txt` and overwrite on passthrough")
	rate := flag.Int("n", 5, "save HAR every `N` seconds")
	addr := flag.String("i", ":6060", "`addr:post` to listen for requests")
	prefix := flag.String("p", "", "`http://hostname/path` prefix to prepend on request paths")
	outname := flag.String("o", "results.har", "output `filename.har` to save proxied requests")
	serverRecorder := flag.Bool("s", false, "use server-side recorder for passthrough requests (less detail)")
	flag.Parse()

	var hits uint32

	var overHeaders http.Header
	if *headerFile != "" {
		raw, err := os.ReadFile(*headerFile)
		if err != nil {
			log.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			parts := strings.SplitN(line, ": ", 2)
			overHeaders.Set(parts[0], parts[1])
		}
	}
	rec := harhar.NewRecorder()
	hcli := http.DefaultClient

	pp, _ := url.Parse(*prefix)
	realHost := pp.Host

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		newurl := *prefix + r.URL.String()
		passthrough, err := http.NewRequest(r.Method, newurl, r.Body)
		if err != nil {
			log.Fatal(err)
		}

		for h, vals := range r.Header {
			if strings.ToLower(h) == "host" {
				passthrough.Header.Add("Host", realHost)
				continue
			}
			for _, val := range vals {
				passthrough.Header.Add(h, val)
			}
		}
		for h := range overHeaders {
			passthrough.Header.Set(h, overHeaders.Get(h))
		}
		resp, err := hcli.Do(passthrough)
		if err != nil {
			log.Fatal(err)
		}

		for h, vals := range resp.Header {
			for _, val := range vals {
				w.Header().Add(h, val)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		atomic.AddUint32(&hits, 1)
	})

	go func() {
		var lasthits uint32
		for range time.NewTicker(time.Second * time.Duration(*rate)).C {
			newhits := atomic.LoadUint32(&hits)
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
		srv.Handler = rec
	} else {
		// since we're proxying every request,
		// client side works great and gets more detail
		hcli = &http.Client{Transport: rec}
	}

	err = srv.Serve(ln)
	if err != nil {
		log.Fatal(err)
	}
}

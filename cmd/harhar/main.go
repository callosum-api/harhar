// Command harhar will do GET requests on provided URLs and log the results to a HAR file.
// This is a simple example that concisely showcases all the features and usage.
//
//		 USAGE: ./harhar [-o results.har] <URL> [<URL>...]
//	   ex: ./harhar https://google.com https://yahoo.com https://bing.com
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/pbnjay/harhar"
)

func main() {
	var (
		output = flag.String("o", "results.har", "output har to `filename`")
	)

	flag.Parse()

	recorder := harhar.NewRecorder()
	client := &http.Client{Transport: recorder}

	for _, u := range flag.Args() {
		resp, err := client.Get(u)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("got %s from %s\n", resp.Status, u)
	}

	size, err := recorder.WriteFile(*output)
	if err != nil {
		log.Fatal(err)
	}

	// it's always good to report size when logging since memory usage
	// will grow pretty quickly if you're not careful.
	log.Printf("wrote %s (%.1fkb)\n", *output, float64(size)/1024.0)
}

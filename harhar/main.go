// This command will do a GET request on a provided URL and log the result to a HAR file.
//  It's a simple example that concisely showcases all the features and usage.

package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	harhar ".."
)

func main() {
	var (
		u      = flag.String("url", "", "url to read")
		output = flag.String("har", "", "output har to file")
	)

	flag.Parse()

	if *u == "" || *output == "" {
		flag.Usage()
		os.Exit(1)
	}

	recorder := harhar.NewRecorder()
	client := &http.Client{Transport: recorder}

	resp, err := client.Get(*u)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("got %s from %s\n", resp.Status, *u)

	size, err := recorder.WriteFile(*output)
	if err != nil {
		log.Fatal(err)
	}

	// it's always good to report size when logging since memory usage
	// will grow pretty quickly if you're not careful.
	log.Printf("wrote %s (%.1fkb)\n", *output, float64(size)/1024.0)
}

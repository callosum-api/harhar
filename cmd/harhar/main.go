// Command harhar fetches URLs with GET requests and records the results to a
// HAR file. It is a small example of the harhar package.
//
//	harhar [-o results.har] [-max-body n] [-timeout 30s] URL...
//
// For example:
//
//	harhar https://google.com https://bing.com
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/callosum-api/harhar"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run runs the command with args and returns its exit code.
func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("harhar", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("o", "results.har", "save the HAR log to `file`")
	maxBody := flags.Int64("max-body", harhar.DefaultMaxBodySize, "record at most `n` bytes of each body (-1 for no limit)")
	timeout := flags.Duration("timeout", 30*time.Second, "give up on each request after `duration`")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: harhar [flags] URL...\n\nFetches each URL and records the results to a HAR file.\n\n")
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

	recorder := harhar.NewRecorder(harhar.WithMaxBodySize(*maxBody))
	client := &http.Client{Transport: recorder.Transport(nil), Timeout: *timeout}

	// Keep going after a failure: failed requests are recorded too.
	failed := 0
	for _, u := range flags.Args() {
		status, err := fetch(client, u)
		if err != nil {
			logger.Print(err)
			failed++
			continue
		}
		logger.Printf("got %s from %s", status, u)
	}

	size, err := recorder.WriteFile(*output)
	if err != nil {
		logger.Print(err)
		return 1
	}

	// it's always good to report size when logging since memory usage
	// will grow pretty quickly if you're not careful.
	logger.Printf("wrote %s (%.1fkb)", *output, float64(size)/1024.0)
	if failed > 0 {
		return 1
	}
	return 0
}

// fetch GETs u and reads the whole response, which completes its entry.
func fetch(client *http.Client, u string) (status string, err error) {
	resp, err := client.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return "", fmt.Errorf("reading %s: %w", u, err)
	}
	return resp.Status, nil
}

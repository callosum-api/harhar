package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callosum-api/harhar"
)

func TestParseHeaders(t *testing.T) {
	// CRLF line endings, a trailing newline, blank lines, colons in values,
	// and a repeated name used to panic or be misparsed.
	in := "Authorization: Bearer abc\r\n\r\nx-trace:  t1 \nX-Trace: t2\nReferer: https://example.com/\n"
	got, err := parseHeaders(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := http.Header{
		"Authorization": {"Bearer abc"},
		"X-Trace":       {"t2"},
		"Referer":       {"https://example.com/"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseHeadersInvalid(t *testing.T) {
	for _, in := range []string{"no colon here\n", ": empty name\n"} {
		if _, err := parseHeaders(strings.NewReader(in)); err == nil {
			t.Errorf("parseHeaders(%q) succeeded, want error", in)
		}
	}
}

func TestProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s host=%s token=%s", r.Method, r.URL.RequestURI(), r.Host, r.Header.Get("X-Token"))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL + "/api")

	var recorded []harhar.Entry
	var mu sync.Mutex
	rec := harhar.NewRecorder(
		harhar.WithHeaderRedactor(headerRedactor(http.Header{"X-Token": {"secret"}})),
		harhar.WithOnEntry(func(e harhar.Entry) { mu.Lock(); recorded = append(recorded, e); mu.Unlock() }),
	)
	proxy := newProxy(target, http.Header{"X-Token": {"secret"}})
	proxy.Transport = rec.Transport(nil)
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/items?q=1", nil)
	req.Header.Set("X-Token", "from-client") // replaced by the -h value
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	u, _ := url.Parse(upstream.URL)
	if want := "GET /api/v1/items?q=1 host=" + u.Host + " token=secret"; string(body) != want {
		t.Errorf("upstream saw %q, want %q", body, want)
	}
	srv.Close() // wait for the proxy handler to finish
	if len(recorded) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(recorded))
	}
	ent := recorded[0]
	if ent.Request.URL != upstream.URL+"/api/v1/items?q=1" {
		t.Errorf("recorded url %q", ent.Request.URL)
	}
	for _, h := range ent.Request.Headers {
		if h.Name == "X-Token" && h.Value != harhar.RedactedValue {
			t.Errorf("X-Token recorded as %q", h.Value)
		}
	}
}

// The proxy passes redirects back to the client instead of following them.
func TestProxyPassesRedirects(t *testing.T) {
	upstream := httptest.NewServer(http.RedirectHandler("/elsewhere", http.StatusFound))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	srv := httptest.NewServer(newProxy(target, nil))
	defer srv.Close()

	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cli.Get(srv.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/elsewhere" {
		t.Errorf("got %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestSecondsOrDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"5": 5 * time.Second, "90s": 90 * time.Second, "1m": time.Minute} {
		var d secondsOrDuration
		if err := d.Set(in); err != nil || time.Duration(d) != want {
			t.Errorf("Set(%q) = %v, %v; want %v", in, time.Duration(d), err, want)
		}
	}
	for _, in := range []string{"0", "-5", "-1s", "soon"} {
		var d secondsOrDuration
		if err := d.Set(in); err == nil {
			t.Errorf("Set(%q) succeeded, want error", in)
		}
	}
}

func TestHeaderRedactor(t *testing.T) {
	redact := headerRedactor(http.Header{"X-Token": {"secret"}, "Authorization": {"Bearer abc"}})
	for _, tc := range []struct{ name, in, want string }{
		{"X-Token", "secret", harhar.RedactedValue},
		{"Authorization", "Bearer abc", "Bearer " + harhar.RedactedValue}, // keeps the default's partial redaction
		{"Cookie", "id=1", "id=" + harhar.RedactedValue},
		{"X-Other", "visible", "visible"},
	} {
		if got := redact(tc.name, tc.in); got != tc.want {
			t.Errorf("redact(%q, %q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

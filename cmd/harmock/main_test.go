package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/callosum-api/harhar"
)

// syncBuffer is a bytes.Buffer safe for the server's logging goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newEntry(method, rawURL string, status int, body string) harhar.Entry {
	return harhar.Entry{
		Request: harhar.Request{Method: method, URL: rawURL},
		Response: harhar.Response{
			Status:  status,
			Headers: []harhar.NameValuePair{{Name: "Content-Type", Value: "application/json"}},
			Content: harhar.Content{Size: int64(len(body)), Text: body},
		},
	}
}

func writeHAR(t *testing.T, entries ...harhar.Entry) string {
	t.Helper()
	h := harhar.NewHAR("test", "1")
	h.Log.Entries = entries
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "recorded.har")
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func sampleHAR(t *testing.T) string {
	postA := newEntry("POST", "https://api.example.com/v1/items", 201, `{"id":1}`)
	postA.Request.PostData = harhar.PostData{MIMEType: "application/json", Text: `{"name":"a"}`}
	postB := newEntry("POST", "https://api.example.com/v1/items", 201, `{"id":2}`)
	postB.Request.PostData = harhar.PostData{MIMEType: "application/json", Text: `{"name":"b"}`}
	down := newEntry("GET", "https://api.example.com/v1/down", 0, "")
	down.Response = harhar.Response{Comment: "dial tcp: connection refused"}
	return writeHAR(t,
		newEntry("GET", "https://api.example.com/v1/items?page=1&_=111", 200, `{"next":"https://api.example.com/v1/items?page=2"}`),
		postA, postB,
		newEntry("GET", "https://api.example.com/v1/jobs/7", 202, `{"state":"pending"}`),
		newEntry("GET", "https://api.example.com/v1/jobs/7", 202, `{"state":"pending"}`),
		newEntry("GET", "https://api.example.com/v1/jobs/7", 200, `{"state":"done"}`),
		newEntry("GET", "https://auth.example.com/token", 200, `{"token":"t"}`),
		down,
	)
}

// start runs the command with args, and returns its URL and a function
// that stops it and returns its exit code.
func start(t *testing.T, args []string, stderr io.Writer) (string, func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	urls := make(chan string, 1)
	code := make(chan int, 1)
	go func() {
		code <- run(ctx, append([]string{"-i", "127.0.0.1:0"}, args...), stderr, func(u string) { urls <- u })
	}()
	select {
	case u := <-urls:
		return u, func() int { cancel(); return <-code }
	case c := <-code:
		cancel()
		t.Fatalf("exited with %d before listening\n%s", c, stderr)
		return "", nil
	}
}

func get(t *testing.T, u string) (int, string) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestRun(t *testing.T) {
	var stderr syncBuffer
	base, stop := start(t, []string{"-ignore-query", "_", sampleHAR(t)}, &stderr)

	if code, body := get(t, base+"/v1/items?_=999&page=1"); code != 200 || body != `{"next":"`+base+`/v1/items?page=2"}` {
		t.Errorf("items: got %d %s", code, body)
	}
	var states []string
	for range 4 {
		_, body := get(t, base+"/v1/jobs/7")
		states = append(states, body)
	}
	if got, want := strings.Join(states, " "), `{"state":"pending"} {"state":"pending"} {"state":"done"} {"state":"done"}`; got != want {
		t.Errorf("job states %s, want %s", got, want)
	}
	resp, err := http.Post(base+"/v1/items", "application/json", strings.NewReader(`{"name":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != `{"id":2}` {
		t.Errorf("POST b: got %s, want the response recorded for b", body)
	}
	if code, _ := get(t, base+"/token"); code != 200 {
		t.Errorf("auth host route: got %d", code)
	}
	if code, body := get(t, base+"/nope"); code != 404 || !strings.Contains(body, "GET /nope") {
		t.Errorf("unmatched: got %d %q", code, body)
	}
	if resp, err := http.Get(base + "/v1/down"); err == nil {
		resp.Body.Close()
		t.Errorf("recorded failure: got %d, want an error", resp.StatusCode)
	}

	if code := stop(); code != 0 {
		t.Errorf("exit code %d", code)
	}
	out := stderr.String()
	t.Log("\n" + out)
	for _, want := range []string{
		"Loaded 8 recorded responses",
		"Routes (5):",
		"GET     /v1/items?page=1  200",
		"POST    /v1/items         201 x2 (by body)",
		"GET     /v1/jobs/7        202, 202, 200",
		"GET     /v1/down          failed",
		"auth.example.com",
		"query parameters ignored: _",
		"api.example.com -> this server",
		"unmatched:  404 Not Found",
		"GET /v1/jobs/7 -> 202",
		"GET /nope -> no recording: 404",
		"GET /v1/down -> connection aborted",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q", want)
		}
	}
}

func TestRunFallbackAndHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "upstream "+r.URL.Path)
	}))
	defer upstream.Close()

	var stderr syncBuffer
	base, stop := start(t, []string{"-q", "-fallback", upstream.URL, "-host", "auth.example.com=auth.local:9000", sampleHAR(t)}, &stderr)
	defer stop()

	if code, body := get(t, base+"/nope"); code != 200 || body != "upstream /nope" {
		t.Errorf("unmatched: got %d %q, want it forwarded", code, body)
	}
	out := stderr.String()
	for _, want := range []string{"forwarded to " + upstream.URL, "auth.example.com -> auth.local:9000 (-host)", "logging:    off"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "GET /nope") {
		t.Errorf("-q still logged requests\n%s", out)
	}
}

func TestRunErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"-host", "nohost", "x.har"}, 2},
		{[]string{"-fallback", "/relative", "x.har"}, 2},
		{[]string{filepath.Join(t.TempDir(), "missing.har")}, 1},
		{[]string{writeHAR(t)}, 1}, // no entries
	} {
		var stderr syncBuffer
		if code := run(context.Background(), tc.args, &stderr, nil); code != tc.code {
			t.Errorf("run(%q) = %d, want %d\n%s", tc.args, code, tc.code, stderr.String())
		}
	}
}

func TestResponses(t *testing.T) {
	for _, tc := range []struct {
		statuses []int
		want     string
	}{
		{[]int{200}, "200"},
		{[]int{200, 200, 200}, "200 x3"},
		{[]int{202, 0, 200}, "202, failed, 200"},
		{[]int{1, 2, 3, 4, 5, 6, 7}, "failed x7"},
		{[]int{201, 202, 203, 204, 205, 206}, "201, 202, 203, 204, 205, ... (6 in all)"},
	} {
		r := &route{statuses: tc.statuses}
		if got := r.responses(); got != tc.want {
			t.Errorf("responses(%v) = %q, want %q", tc.statuses, got, tc.want)
		}
	}
}

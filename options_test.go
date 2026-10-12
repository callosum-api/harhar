package harhar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// newOptionsClient is newTestClient with Recorder options.
func newOptionsClient(t *testing.T, h http.HandlerFunc, opts ...Option) (*httptest.Server, *http.Client, *Recorder) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	rec := NewRecorder(opts...)
	return srv, &http.Client{Transport: rec.Transport(nil)}, rec
}

func TestDefaultRedaction(t *testing.T) {
	srv, cli, rec := newOptionsClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "s3cret", Path: "/"})
		w.Header().Set("X-Other", "visible")
		// The server still receives the real values.
		io.WriteString(w, r.Header.Get("Authorization")+" "+r.Header.Get("Cookie"))
	})
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer tok")
	req.AddCookie(&http.Cookie{Name: "id", Value: "abc"})
	if got := string(mustDo(t, cli, req)); got != "Bearer tok id=abc" {
		t.Errorf("server saw %q", got)
	}

	ent := onlyEntry(t, rec)
	if got := headerValues(ent.Request.Headers, "Authorization"); !slices.Equal(got, []string{RedactedValue}) {
		t.Errorf("Authorization recorded as %q", got)
	}
	if got := headerValues(ent.Request.Headers, "Cookie"); !slices.Equal(got, []string{RedactedValue}) {
		t.Errorf("Cookie recorded as %q", got)
	}
	if c := ent.Request.Cookies; len(c) != 1 || c[0].Name != "id" || c[0].Value != RedactedValue {
		t.Errorf("request cookies = %+v", c)
	}
	if got := headerValues(ent.Response.Headers, "Set-Cookie"); !slices.Equal(got, []string{RedactedValue}) {
		t.Errorf("Set-Cookie recorded as %q", got)
	}
	if c := ent.Response.Cookies; len(c) != 1 || c[0].Name != "session" || c[0].Value != RedactedValue || c[0].Path != "/" {
		t.Errorf("response cookies = %+v", c)
	}
	if got := headerValues(ent.Response.Headers, "X-Other"); !slices.Equal(got, []string{"visible"}) {
		t.Errorf("X-Other recorded as %q", got)
	}
}

func TestWithRedactedHeaders(t *testing.T) {
	for _, tc := range []struct {
		names        []string
		apiKey, auth string
	}{
		{[]string{"x-api-key"}, RedactedValue, "Bearer tok"},
		{nil, "key", "Bearer tok"},
	} {
		srv, cli, rec := newOptionsClient(t, echo, WithRedactedHeaders(tc.names...))
		req, _ := http.NewRequest("GET", srv.URL, nil)
		req.Header.Set("X-Api-Key", "key")
		req.Header.Set("Authorization", "Bearer tok")
		mustDo(t, cli, req)

		headers := onlyEntry(t, rec).Request.Headers
		if got := headerValues(headers, "X-Api-Key"); !slices.Equal(got, []string{tc.apiKey}) {
			t.Errorf("WithRedactedHeaders(%q): X-Api-Key = %q", tc.names, got)
		}
		if got := headerValues(headers, "Authorization"); !slices.Equal(got, []string{tc.auth}) {
			t.Errorf("WithRedactedHeaders(%q): Authorization = %q", tc.names, got)
		}
	}
}

func TestWithMaxBodySize(t *testing.T) {
	srv, cli, rec := newOptionsClient(t, echo, WithMaxBodySize(4))
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("abcdefgh"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := string(mustDo(t, cli, req)); got != "abcdefgh" {
		t.Errorf("traffic was truncated: got %q", got)
	}

	ent := onlyEntry(t, rec)
	pd := ent.Request.PostData
	// A truncated form is kept as text rather than parsed.
	if pd.Text != "abcd" || pd.Params != nil || ent.Request.BodySize != 8 || pd.Comment == "" {
		t.Errorf("request: bodySize %d, postData %+v", ent.Request.BodySize, pd)
	}
	c := ent.Response.Content
	if c.Text != "abcd" || c.Size != 8 || ent.Response.BodySize != 8 || c.Comment == "" {
		t.Errorf("response: bodySize %d, content %+v", ent.Response.BodySize, c)
	}
}

func TestWithMaxBodySizeKeepsUTF8Valid(t *testing.T) {
	srv, cli, rec := newOptionsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "héllo") // é is two bytes
	}, WithMaxBodySize(2))
	req, _ := http.NewRequest("GET", srv.URL, nil)
	mustDo(t, cli, req)
	if c := onlyEntry(t, rec).Response.Content; c.Text != "h" || c.Encoding != "" {
		t.Errorf("content = %+v, want text \"h\" without a split rune", c)
	}
}

func TestWithMaxBodySizeZeroAndUnlimited(t *testing.T) {
	big := strings.Repeat("x", DefaultMaxBodySize+1)
	handler := func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, big) }
	for _, tc := range []struct {
		opts []Option
		want int
	}{
		{nil, DefaultMaxBodySize},
		{[]Option{WithMaxBodySize(0)}, 0},
		{[]Option{WithMaxBodySize(-1)}, len(big)},
	} {
		srv, cli, rec := newOptionsClient(t, handler, tc.opts...)
		req, _ := http.NewRequest("GET", srv.URL, nil)
		mustDo(t, cli, req)
		c := onlyEntry(t, rec).Response.Content
		if len(c.Text) != tc.want || c.Size != int64(len(big)) {
			t.Errorf("want %d bytes kept: kept %d, size %d", tc.want, len(c.Text), c.Size)
		}
	}
}

func TestWithMaxEntries(t *testing.T) {
	srv, cli, rec := newOptionsClient(t, echo, WithMaxEntries(2))
	paths := func() []string {
		var got []string
		for _, e := range rec.HAR().Log.Entries {
			got = append(got, strings.TrimPrefix(e.Request.URL, srv.URL))
		}
		return got
	}
	for i := 1; i <= 5; i++ {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/%d", srv.URL, i), nil)
		mustDo(t, cli, req)
		if i == 3 {
			if got := paths(); !slices.Equal(got, []string{"/2", "/3"}) {
				t.Errorf("after 3 requests: %q", got)
			}
		}
	}
	if got := paths(); !slices.Equal(got, []string{"/4", "/5"}) {
		t.Errorf("after 5 requests: %q", got)
	}
}

func TestWithOnEntryAndNoStorage(t *testing.T) {
	var calls atomic.Int32
	srv, cli, rec := newOptionsClient(t, echo, WithMaxEntries(0), WithOnEntry(func(e Entry) {
		if e.Response.Status == http.StatusOK {
			calls.Add(1)
		}
	}))
	for range 3 {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		mustDo(t, cli, req)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("OnEntry called %d times, want 3", n)
	}
	if n := len(rec.HAR().Log.Entries); n != 0 {
		t.Errorf("%d entries stored, want 0", n)
	}
}

func TestCreatorAndReset(t *testing.T) {
	if c := NewRecorder().HAR().Log.Creator; c.Name != "harhar" || c.Version == "" {
		t.Errorf("default creator = %+v", c)
	}
	srv, cli, rec := newOptionsClient(t, echo, WithCreator("myapp", "1.0"))
	req, _ := http.NewRequest("GET", srv.URL, nil)
	mustDo(t, cli, req)
	if c := rec.HAR().Log.Creator; c != (Creator{Name: "myapp", Version: "1.0"}) {
		t.Errorf("creator = %+v", c)
	}
	rec.Reset()
	if h := rec.HAR(); len(h.Log.Entries) != 0 || h.Log.Entries == nil {
		t.Errorf("after Reset: entries = %#v, want empty and non-nil", h.Log.Entries)
	}
}

// WriteTo's output can be read back with the same types and re-encodes
// identically.
func TestWriteToRoundTrip(t *testing.T) {
	srv, cli, rec := newOptionsClient(t, echo)
	req, _ := http.NewRequest("POST", srv.URL+"?q=1", strings.NewReader("a=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "c", Value: "v"})
	mustDo(t, cli, req)

	var buf bytes.Buffer
	n, err := rec.WriteTo(&buf)
	if err != nil || n != int64(buf.Len()) {
		t.Fatalf("WriteTo = %d, %v; wrote %d bytes", n, err, buf.Len())
	}
	var h HAR
	if err := json.Unmarshal(buf.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(&h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, buf.Bytes()) {
		t.Errorf("round trip changed the output:\n%s\n%s", buf.Bytes(), again)
	}
}

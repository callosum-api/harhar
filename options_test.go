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
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "s3cret", Path: "/", HttpOnly: true})
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
	for _, tc := range []struct {
		pairs      []NameValuePair
		name, want string
	}{
		{ent.Request.Headers, "Authorization", "Bearer " + RedactedValue},
		{ent.Request.Headers, "Cookie", "id=" + RedactedValue},
		{ent.Response.Headers, "Set-Cookie", "session=" + RedactedValue + "; Path=/; HttpOnly"},
		{ent.Response.Headers, "X-Other", "visible"},
	} {
		if got := headerValues(tc.pairs, tc.name); !slices.Equal(got, []string{tc.want}) {
			t.Errorf("%s recorded as %q, want %q", tc.name, got, tc.want)
		}
	}
	// Cookie names and attributes are kept; only values are redacted.
	if got, want := ent.Request.Cookies, []Cookie{{Name: "id", Value: RedactedValue}}; !slices.Equal(got, want) {
		t.Errorf("request cookies = %+v, want %+v", got, want)
	}
	want := []Cookie{{Name: "session", Value: RedactedValue, Path: "/", HTTPOnly: true}}
	if got := ent.Response.Cookies; !slices.Equal(got, want) {
		t.Errorf("response cookies = %+v, want %+v", got, want)
	}
}

func TestWithHeaderRedactor(t *testing.T) {
	// Redact an extra header fully, leave cookies alone, and use the
	// default for everything else.
	redactor := func(name, value string) string {
		switch name {
		case "X-Api-Key":
			return RedactedValue
		case "Cookie":
			return value
		}
		return DefaultHeaderRedactor(name, value)
	}
	for _, tc := range []struct {
		desc                    string
		fn                      func(string, string) string
		apiKey, auth, cookieVal string
	}{
		{"custom", redactor, RedactedValue, "Bearer " + RedactedValue, "abc"},
		{"nil", nil, "key", "Bearer tok", "abc"},
	} {
		srv, cli, rec := newOptionsClient(t, echo, WithHeaderRedactor(tc.fn))
		req, _ := http.NewRequest("GET", srv.URL, nil)
		req.Header.Set("X-Api-Key", "key")
		req.Header.Set("Authorization", "Bearer tok")
		req.AddCookie(&http.Cookie{Name: "id", Value: "abc"})
		mustDo(t, cli, req)

		r := onlyEntry(t, rec).Request
		if got := headerValues(r.Headers, "X-Api-Key"); !slices.Equal(got, []string{tc.apiKey}) {
			t.Errorf("%s: X-Api-Key = %q", tc.desc, got)
		}
		if got := headerValues(r.Headers, "Authorization"); !slices.Equal(got, []string{tc.auth}) {
			t.Errorf("%s: Authorization = %q", tc.desc, got)
		}
		if len(r.Cookies) != 1 || r.Cookies[0].Value != tc.cookieVal {
			t.Errorf("%s: cookies = %+v", tc.desc, r.Cookies)
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
	if got := NewRecorder().cfg.maxBodySize; got != DefaultMaxBodySize || DefaultMaxBodySize != 32<<20 {
		t.Errorf("default max body size = %d, want 32 MiB", got)
	}
	body := strings.Repeat("x", 1000)
	handler := func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }
	for _, tc := range []struct {
		max  int64
		want int
	}{
		{999, 999},
		{0, 0},
		{-1, len(body)},
	} {
		srv, cli, rec := newOptionsClient(t, handler, WithMaxBodySize(tc.max))
		req, _ := http.NewRequest("GET", srv.URL, nil)
		mustDo(t, cli, req)
		c := onlyEntry(t, rec).Response.Content
		if len(c.Text) != tc.want || c.Size != int64(len(body)) {
			t.Errorf("WithMaxBodySize(%d): kept %d bytes of %d, want %d", tc.max, len(c.Text), c.Size, tc.want)
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

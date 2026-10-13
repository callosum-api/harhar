package mock_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/callosum-api/harhar"
	"github.com/callosum-api/harhar/mock"
)

// newEntry returns an entry for a request to rawURL answered with status
// and body. headers are name, value pairs.
func newEntry(method, rawURL string, status int, body string, headers ...string) harhar.Entry {
	e := harhar.Entry{
		Request: harhar.Request{Method: method, URL: rawURL, HTTPVersion: "HTTP/1.1"},
		Response: harhar.Response{
			Status:      status,
			StatusText:  http.StatusText(status),
			HTTPVersion: "HTTP/1.1",
			Content:     harhar.Content{Size: int64(len(body)), Text: body},
		},
	}
	for i := 0; i+1 < len(headers); i += 2 {
		e.Response.Headers = append(e.Response.Headers, harhar.NameValuePair{Name: headers[i], Value: headers[i+1]})
	}
	return e
}

func newHAR(entries ...harhar.Entry) *harhar.HAR {
	h := harhar.NewHAR("test", "1")
	h.Log.Entries = entries
	return h
}

// do makes a request and returns the response with its body read.
func do(t *testing.T, client *http.Client, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func get(t *testing.T, client *http.Client, rawURL string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return do(t, client, req)
}

func TestTransportReplaysRecording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		if r.Method == "POST" {
			r.ParseForm()
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, "created %s", r.PostForm.Get("name"))
			return
		}
		fmt.Fprintf(w, "hello from %s", r.URL.Path)
	}))
	rec := harhar.NewRecorder()
	live := &http.Client{Transport: rec.Transport(nil)}
	get(t, live, srv.URL+"/hello")
	resp, err := live.PostForm(srv.URL+"/items", url.Values{"name": {"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	srv.Close() // the mock must not need the server

	m := mock.New(rec.HAR())
	client := &http.Client{Transport: m.Transport(nil)}

	resp, body := get(t, client, srv.URL+"/hello")
	if resp.StatusCode != 200 || body != "hello from /hello" {
		t.Errorf("got %d %q, want 200 %q", resp.StatusCode, body, "hello from /hello")
	}
	if got := resp.Header.Get("X-Test"); got != "yes" {
		t.Errorf("X-Test = %q, want yes", got)
	}
	if resp.ContentLength != int64(len(body)) || resp.Header.Get("Content-Length") != fmt.Sprint(len(body)) {
		t.Errorf("ContentLength = %d, header %q, want %d", resp.ContentLength, resp.Header.Get("Content-Length"), len(body))
	}

	resp, err = client.PostForm(srv.URL+"/items", url.Values{"name": {"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 || resp.Status != "201 Created" || string(b) != "created widget" {
		t.Errorf("got %q %q, want 201 Created, %q", resp.Status, b, "created widget")
	}
	if unused := m.Unused(); len(unused) != 0 {
		t.Errorf("%d entries unused", len(unused))
	}
}

func TestTransportNoMatch(t *testing.T) {
	m := mock.New(newHAR(newEntry("GET", "https://api.example.com/a?x=1", 200, "a")))
	client := &http.Client{Transport: m.Transport(nil)}

	for _, u := range []string{
		"https://api.example.com/b",
		"https://api.example.com/a",
		"https://api.example.com/a?x=2",
		"https://other.example.com/a?x=1",
	} {
		_, err := client.Get(u)
		if !errors.Is(err, mock.ErrNoMatch) {
			t.Errorf("GET %s: err = %v, want ErrNoMatch", u, err)
		}
	}
	if _, err := client.Post("https://api.example.com/a?x=1", "text/plain", nil); !errors.Is(err, mock.ErrNoMatch) {
		t.Errorf("POST: err = %v, want ErrNoMatch", err)
	}

	// Host and default port are compared case-insensitively.
	if resp, body := get(t, client, "https://API.example.com:443/a?x=1"); body != "a" {
		t.Errorf("got %d %q, want a", resp.StatusCode, body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportFallback(t *testing.T) {
	m := mock.New(newHAR(newEntry("GET", "https://api.example.com/a", 200, "mocked")))
	var gotBody string
	fallback := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("real")), Request: r}, nil
	})
	client := &http.Client{Transport: m.Transport(fallback)}

	if _, body := get(t, client, "https://api.example.com/a"); body != "mocked" {
		t.Errorf("matched request: got %q, want mocked", body)
	}
	req, _ := http.NewRequest("POST", "https://api.example.com/b", strings.NewReader("payload"))
	if _, body := do(t, client, req); body != "real" {
		t.Errorf("unmatched request: got %q, want real", body)
	}
	if gotBody != "payload" {
		t.Errorf("fallback got body %q, want payload", gotBody)
	}
}

func TestMiddlewareRewritesHosts(t *testing.T) {
	const next = `{"self":"https://api.example.com/next",` +
		`"escaped":"https:\/\/api.example.com\/x",` +
		`"relative":"//api.example.com/y",` +
		`"evil":"https://api.example.com.evil.net/",` +
		`"port":"https://api.example.com:8443/",` +
		`"other":"https://cdn.example.org/z"}`
	m := mock.New(newHAR(
		newEntry("GET", "https://api.example.com/start", 302, "",
			"Location", "https://api.example.com/next",
			"Set-Cookie", "id=[REDACTED]; Domain=example.com; Path=/",
			"Set-Cookie", "other=1; Domain=example.org"),
		newEntry("GET", "https://api.example.com/next", 200, next,
			"Content-Type", "application/json",
			"Link", `<https://api.example.com/next?page=2>; rel="next"`),
	))
	srv := httptest.NewServer(m.Middleware(nil))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	// Without following the redirect.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, _ := get(t, noRedirect, srv.URL+"/start")
	if got, want := resp.Header.Get("Location"), srv.URL+"/next"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	wantCookies := []string{"id=[REDACTED]; Path=/", "other=1; Domain=example.org"}
	if got := resp.Header.Values("Set-Cookie"); strings.Join(got, "\n") != strings.Join(wantCookies, "\n") {
		t.Errorf("Set-Cookie = %q, want %q", got, wantCookies)
	}

	// Following it, which only works if the Location was rewritten.
	resp, body := get(t, srv.Client(), srv.URL+"/start")
	want := `{"self":"` + srv.URL + `/next",` +
		`"escaped":"http:\/\/` + host + `\/x",` +
		`"relative":"//` + host + `/y",` +
		`"evil":"https://api.example.com.evil.net/",` +
		`"port":"https://api.example.com:8443/",` +
		`"other":"https://cdn.example.org/z"}`
	if resp.StatusCode != 200 || body != want {
		t.Errorf("got %d\n%s\nwant 200\n%s", resp.StatusCode, body, want)
	}
	if got, want := resp.Header.Get("Link"), `<`+srv.URL+`/next?page=2>; rel="next"`; got != want {
		t.Errorf("Link = %q, want %q", got, want)
	}
}

func TestMiddlewareNoMatch(t *testing.T) {
	m := mock.New(newHAR(newEntry("GET", "https://api.example.com/a", 200, "a")))
	srv := httptest.NewServer(m.Middleware(nil))
	resp, body := get(t, srv.Client(), srv.URL+"/b")
	srv.Close()
	if resp.StatusCode != 404 || !strings.Contains(body, "GET /b") {
		t.Errorf("got %d %q, want 404 naming the request", resp.StatusCode, body)
	}

	var gotBody string
	srv = httptest.NewServer(m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, "next")
	})))
	defer srv.Close()
	req, _ := http.NewRequest("PUT", srv.URL+"/a", strings.NewReader("payload"))
	if _, body := do(t, srv.Client(), req); body != "next" {
		t.Errorf("got %q, want next", body)
	}
	if gotBody != "payload" {
		t.Errorf("next got body %q, want payload", gotBody)
	}
}

func TestMiddlewarePrefersRequestHost(t *testing.T) {
	m := mock.New(newHAR(
		newEntry("GET", "https://a.example.com/x", 200, "a"),
		newEntry("GET", "https://b.example.com/x", 200, "b"),
	))
	srv := httptest.NewServer(m.Middleware(nil))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/x", nil)
	req.Host = "b.example.com"
	if _, body := do(t, srv.Client(), req); body != "b" {
		t.Errorf("Host b.example.com: got %q, want b", body)
	}
	// Any other host gets the entries in order.
	if _, body := get(t, srv.Client(), srv.URL+"/x"); body != "a" {
		t.Errorf("got %q, want a", body)
	}
}

func TestSequence(t *testing.T) {
	m := mock.New(newHAR(
		newEntry("GET", "https://api.example.com/job", 200, "pending"),
		newEntry("GET", "https://api.example.com/other", 200, "other"),
		newEntry("GET", "https://api.example.com/job", 200, "running"),
		newEntry("GET", "https://api.example.com/job", 200, "done"),
	))
	client := &http.Client{Transport: m.Transport(nil)}

	var got []string
	for range 5 {
		_, body := get(t, client, "https://api.example.com/job")
		got = append(got, body)
	}
	if want := "pending running done done done"; strings.Join(got, " ") != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if unused := m.Unused(); len(unused) != 1 || unused[0].Response.Content.Text != "other" {
		t.Errorf("Unused() = %v, want only /other", unused)
	}

	m.Reset()
	if _, body := get(t, client, "https://api.example.com/job"); body != "pending" {
		t.Errorf("after Reset: got %q, want pending", body)
	}
	if n := len(m.Unused()); n != 3 {
		t.Errorf("after Reset: %d unused, want 3", n)
	}
}

func TestPrefersMatchingBody(t *testing.T) {
	a := newEntry("POST", "https://api.example.com/graphql", 200, "A")
	a.Request.PostData = harhar.PostData{MIMEType: "application/json", Text: `{"query":"a"}`}
	b := newEntry("POST", "https://api.example.com/graphql", 200, "B")
	b.Request.PostData = harhar.PostData{MIMEType: "application/json", Text: `{"query":"b"}`}
	m := mock.New(newHAR(a, b))
	client := &http.Client{Transport: m.Transport(nil)}

	post := func(body string) string {
		req, _ := http.NewRequest("POST", "https://api.example.com/graphql", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		_, got := do(t, client, req)
		return got
	}
	if got := post(`{"query":"b"}`); got != "B" {
		t.Errorf("body b: got %q, want B", got)
	}
	if got := post(`{"query":"a"}`); got != "A" {
		t.Errorf("body a: got %q, want A", got)
	}
	// No body matches, so it falls back to the recorded order.
	if got := post(`{"query":"c"}`); got != "B" {
		t.Errorf("body c: got %q, want B (the last, as all were used)", got)
	}
}

func TestPrefersMatchingMultipartForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		io.WriteString(w, "got "+r.FormValue("name"))
	}))
	rec := harhar.NewRecorder()
	live := &http.Client{Transport: rec.Transport(nil)}
	upload := func(client *http.Client, name string) string {
		var buf strings.Builder
		mw := multipart.NewWriter(&buf) // a new random boundary each time
		mw.WriteField("name", name)
		mw.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/upload", strings.NewReader(buf.String()))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		_, body := do(t, client, req)
		return body
	}
	upload(live, "first")
	upload(live, "second")
	srv.Close()

	client := &http.Client{Transport: mock.New(rec.HAR()).Transport(nil)}
	if got := upload(client, "second"); got != "got second" {
		t.Errorf("got %q, want %q", got, "got second")
	}
}

func TestQueryOrder(t *testing.T) {
	m := mock.New(newHAR(newEntry("GET", "https://api.example.com/s?a=1&b=2&b=3", 200, "ok")))
	client := &http.Client{Transport: m.Transport(nil)}
	if _, body := get(t, client, "https://api.example.com/s?b=2&a=1&b=3"); body != "ok" {
		t.Errorf("got %q, want ok", body)
	}
	if _, err := client.Get("https://api.example.com/s?a=1&b=3&b=2"); !errors.Is(err, mock.ErrNoMatch) {
		t.Errorf("repeated values reordered: err = %v, want ErrNoMatch", err)
	}
}

func TestWithHost(t *testing.T) {
	m := mock.New(newHAR(
		newEntry("GET", "https://api.example.com/x", 200, `see https://api.example.com/y and https://auth.example.com/login`,
			"Location", "https://auth.example.com/login"),
	),
		mock.WithHost("api.example.com", "staging.local:8080"),
		mock.WithHost("auth.example.com", "auth.staging.local"),
	)
	client := &http.Client{Transport: m.Transport(nil)}

	resp, body := get(t, client, "http://staging.local:8080/x")
	if want := `see http://staging.local:8080/y and https://auth.staging.local/login`; body != want {
		t.Errorf("got %q, want %q", body, want)
	}
	if got, want := resp.Header.Get("Location"), "https://auth.staging.local/login"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	// The recorded host now maps elsewhere.
	if _, err := client.Get("https://api.example.com/x"); !errors.Is(err, mock.ErrNoMatch) {
		t.Errorf("recorded host: err = %v, want ErrNoMatch", err)
	}
}

func TestWithMatcher(t *testing.T) {
	m := mock.New(newHAR(newEntry("GET", "https://api.example.com/s?q=go&_=123", 200, "ok")),
		mock.WithMatcher(func(req *http.Request, e harhar.Entry) bool {
			r := req.Clone(req.Context())
			q := r.URL.Query()
			q.Del("_")
			r.URL.RawQuery = q.Encode()
			u, _ := url.Parse(e.Request.URL)
			eq := u.Query()
			eq.Del("_")
			u.RawQuery = eq.Encode()
			e.Request.URL = u.String()
			return mock.DefaultMatcher(r, e)
		}))
	client := &http.Client{Transport: m.Transport(nil)}
	if _, body := get(t, client, "https://api.example.com/s?_=456&q=go"); body != "ok" {
		t.Errorf("got %q, want ok", body)
	}
}

func TestRecordedFailure(t *testing.T) {
	failed := harhar.Entry{
		Request:  harhar.Request{Method: "GET", URL: "https://api.example.com/down"},
		Response: harhar.Response{Comment: "dial tcp: connection refused"},
	}
	m := mock.New(newHAR(failed))

	client := &http.Client{Transport: m.Transport(nil)}
	if _, err := client.Get("https://api.example.com/down"); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("transport: err = %v, want the recorded error", err)
	}

	srv := httptest.NewServer(m.Middleware(nil))
	defer srv.Close()
	if resp, err := srv.Client().Get(srv.URL + "/down"); err == nil {
		resp.Body.Close()
		t.Errorf("server: got %d, want an error", resp.StatusCode)
	}
}

func TestBinaryBody(t *testing.T) {
	data := []byte("\xff\x00https://api.example.com/\xfe")
	e := newEntry("GET", "https://api.example.com/img", 200, "", "Content-Type", "image/png")
	e.Response.Content = harhar.Content{Size: int64(len(data)), Text: base64.StdEncoding.EncodeToString(data), Encoding: "base64"}
	m := mock.New(newHAR(e))
	srv := httptest.NewServer(m.Middleware(nil))
	defer srv.Close()

	if _, body := get(t, srv.Client(), srv.URL+"/img"); body != string(data) {
		t.Errorf("got %q, want %q unchanged", body, data)
	}
}

func TestHead(t *testing.T) {
	m := mock.New(newHAR(newEntry("HEAD", "https://api.example.com/a", 200, "", "Content-Type", "text/plain")))
	client := &http.Client{Transport: m.Transport(nil)}
	resp, err := client.Head("https://api.example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("got %d %v", resp.StatusCode, resp.Header)
	}
}

func TestLoad(t *testing.T) {
	rec := harhar.NewRecorder()
	srv := httptest.NewServer(rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "saved")
	})))
	get(t, srv.Client(), srv.URL+"/thing")
	srv.Close()

	filename := filepath.Join(t.TempDir(), "out.har")
	if _, err := rec.WriteFile(filename); err != nil {
		t.Fatal(err)
	}
	m, err := mock.Load(filename)
	if err != nil {
		t.Fatal(err)
	}
	mockSrv := httptest.NewServer(m.Middleware(nil))
	defer mockSrv.Close()
	if _, body := get(t, mockSrv.Client(), mockSrv.URL+"/thing"); body != "saved" {
		t.Errorf("got %q, want saved", body)
	}

	if _, err := mock.Load(filepath.Join(t.TempDir(), "missing.har")); err == nil {
		t.Error("Load of a missing file succeeded")
	}
}

func TestConcurrent(t *testing.T) {
	var entries []harhar.Entry
	for i := range 10 {
		entries = append(entries, newEntry("GET", "https://api.example.com/n", 200, fmt.Sprint(i)))
	}
	m := mock.New(newHAR(entries...))
	client := &http.Client{Transport: m.Transport(nil)}

	var wg sync.WaitGroup
	results := make(chan string, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get("https://api.example.com/n")
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			results <- string(b)
		}()
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for r := range results {
		seen[r] = true
	}
	if len(seen) != 10 {
		t.Errorf("got %d distinct responses, want each of the 10 once", len(seen))
	}
}

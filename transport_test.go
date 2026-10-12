package harhar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// unblocker returns a channel and a function that closes it once, also
// called at cleanup so that blocked test handlers always finish.
func unblocker(t *testing.T) (<-chan struct{}, func()) {
	ch := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	return ch, release
}

// The response body streams through to the caller instead of being read
// in full first, and the entry is recorded once the body is done.
func TestTransportStreamsResponse(t *testing.T) {
	wait, release := unblocker(t)
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "first ")
		w.(http.Flusher).Flush()
		<-wait
		io.WriteString(w, "second")
	})

	resp, err := cli.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(resp.Body, make([]byte, len("first ")))
		first <- err
	}()
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first chunk was not delivered until the response finished")
	}
	if n := len(rec.HAR().Log.Entries); n != 0 {
		t.Errorf("%d entries recorded before the body was read", n)
	}

	release()
	io.Copy(io.Discard, resp.Body)
	if got := onlyEntry(t, rec).Response.Content.Text; got != "first second" {
		t.Errorf("recorded body %q", got)
	}
}

func TestTransportBodyClosedEarly(t *testing.T) {
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", 100))
	})

	// Stopping at Content-Length bytes without seeing EOF is complete...
	resp, err := cli.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadFull(resp.Body, make([]byte, 100))
	resp.Body.Close()
	if c := onlyEntry(t, rec).Response.Comment; c != "" {
		t.Errorf("complete body has comment %q", c)
	}

	// ...but closing partway through is noted.
	rec.Reset()
	resp, err = cli.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadFull(resp.Body, make([]byte, 10))
	resp.Body.Close()
	ent := onlyEntry(t, rec)
	if ent.Response.Comment != errClosedEarly.Error() || ent.Response.Content.Size != 10 {
		t.Errorf("comment %q, size %d", ent.Response.Comment, ent.Response.Content.Size)
	}
}

// Responses without a body are recorded even if the caller never closes them.
func TestTransportHeadRecordedImmediately(t *testing.T) {
	srv, cli, rec := newTestClient(t, echo)
	resp, err := cli.Head(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ent := onlyEntry(t, rec); ent.Request.Method != "HEAD" {
		t.Errorf("method = %q", ent.Request.Method)
	}
}

func TestTransportHTTP2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(echo))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	rec := NewRecorder()
	cli := &http.Client{Transport: rec.Transport(srv.Client().Transport)}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	mustDo(t, cli, req)

	ent := onlyEntry(t, rec)
	if ent.Request.HTTPVersion != "HTTP/2.0" || ent.Response.HTTPVersion != "HTTP/2.0" {
		t.Errorf("httpVersion = %q / %q, want HTTP/2.0", ent.Request.HTTPVersion, ent.Response.HTTPVersion)
	}
	if tm := ent.Timings; tm.SSL < 0 || tm.Connect < tm.SSL {
		t.Errorf("want 0 <= ssl <= connect, got ssl %v, connect %v", tm.SSL, tm.Connect)
	}
}

func TestTransportTimingsOnReusedConnection(t *testing.T) {
	srv, cli, rec := newTestClient(t, echo)
	for range 2 {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		mustDo(t, cli, req)
	}
	entries := rec.HAR().Log.Entries
	first, second := entries[0].Timings, entries[1].Timings
	if first.Connect < 0 || first.DNS != -1 || first.SSL != -1 {
		t.Errorf("new connection to an IP: %+v", first)
	}
	if second.Connect != -1 || second.DNS != -1 || second.SSL != -1 || second.Blocked < 0 {
		t.Errorf("reused connection: %+v", second)
	}
	for i, ent := range entries {
		if ent.Time != ent.Timings.total() {
			t.Errorf("entry %d: time %v != sum of timings %v", i, ent.Time, ent.Timings.total())
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A base transport that doesn't report httptrace events still gets timings.
func TestTransportCustomBase(t *testing.T) {
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		time.Sleep(20 * time.Millisecond)
		return &http.Response{
			StatusCode: http.StatusOK,
			Proto:      "HTTP/1.1",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    r,
		}, nil
	})
	rec := NewRecorder()
	req, _ := http.NewRequest("GET", "http://example.invalid/", nil)
	mustDo(t, &http.Client{Transport: rec.Transport(base)}, req)

	ent := onlyEntry(t, rec)
	if ent.Timings.Wait < 20 || ent.Timings.Blocked != -1 {
		t.Errorf("timings = %+v", ent.Timings)
	}
	if ent.Response.Content.Text != "ok" {
		t.Errorf("content = %+v", ent.Response.Content)
	}
}

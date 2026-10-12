package harhar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// newTestServer serves h through the recording middleware.
func newTestServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Recorder) {
	t.Helper()
	rec := NewRecorder()
	srv := httptest.NewServer(rec.Middleware(h))
	t.Cleanup(srv.Close)
	return srv, rec
}

func serverGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func headerValues(pairs []NameValuePair, name string) []string {
	var vals []string
	for _, p := range pairs {
		if http.CanonicalHeaderKey(p.Name) == name {
			vals = append(vals, p.Value)
		}
	}
	return vals
}

// An empty handler used to make the recorder panic with WriteHeader(0).
func TestMiddlewareEmptyHandler(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if resp := serverGet(t, srv.URL); resp.StatusCode != http.StatusOK {
		t.Errorf("client got status %d, want 200", resp.StatusCode)
	}
	if got := onlyEntry(t, rec).Response.Status; got != http.StatusOK {
		t.Errorf("recorded status %d, want 200", got)
	}
}

// Multi-valued headers used to be collapsed to their last value.
func TestMiddlewareMultiValueHeaders(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Multi", "a")
		w.Header().Add("X-Multi", "b")
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
	})
	resp := serverGet(t, srv.URL)
	if got, want := resp.Header.Values("X-Multi"), []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("client got X-Multi %q, want %q", got, want)
	}
	if got, want := resp.Header.Values("Set-Cookie"), []string{"a=1", "b=2"}; !slices.Equal(got, want) {
		t.Errorf("client got Set-Cookie %q, want %q", got, want)
	}
	ent := onlyEntry(t, rec)
	if got, want := headerValues(ent.Response.Headers, "X-Multi"), []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("recorded X-Multi %q, want %q", got, want)
	}
	if len(ent.Response.Cookies) != 2 {
		t.Errorf("recorded %d cookies, want 2", len(ent.Response.Cookies))
	}
}

func TestMiddlewareWriteHeaderSemantics(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</style.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints) // informational, not final
		w.WriteHeader(http.StatusCreated)
		w.WriteHeader(http.StatusInternalServerError) // superfluous, ignored
		w.Header().Set("X-Late", "ignored")           // after WriteHeader, ignored
		io.WriteString(w, "ok")
	})
	resp := serverGet(t, srv.URL)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("client got status %d, want 201", resp.StatusCode)
	}
	if resp.Header.Get("X-Late") != "" {
		t.Error("client got a header set after WriteHeader")
	}
	ent := onlyEntry(t, rec)
	if ent.Response.Status != http.StatusCreated {
		t.Errorf("recorded status %d, want 201", ent.Response.Status)
	}
	if len(headerValues(ent.Response.Headers, "X-Late")) != 0 {
		t.Error("recorded a header set after WriteHeader")
	}
}

func TestMiddlewareTimingsAndServerIP(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	serverGet(t, srv.URL)
	ent := onlyEntry(t, rec)
	// HAR requires send, wait and receive to be non-negative.
	if tm := ent.Timings; tm.Send < 0 || tm.Wait < 0 || tm.Receive < 0 {
		t.Errorf("negative timings: %+v", tm)
	}
	if ent.ServerIP != "127.0.0.1" {
		t.Errorf("serverIPAddress = %q, want 127.0.0.1", ent.ServerIP)
	}
}

// Responses stream through the middleware, and http.ResponseController
// reaches the real ResponseWriter.
func TestMiddlewareStreamsResponse(t *testing.T) {
	wait, release := unblocker(t)
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
			t.Errorf("SetWriteDeadline: %v", err)
		}
		io.WriteString(w, "first ")
		if err := rc.Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
		<-wait
		io.WriteString(w, "second")
	})

	resp, err := http.Get(srv.URL)
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
		t.Fatal("first chunk was not delivered until the handler finished")
	}
	release()
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	srv.Close() // wait for the handler, and so the entry, to finish
	ent := onlyEntry(t, rec)
	if ent.Response.Content.Text != "first second" {
		t.Errorf("recorded body %q", ent.Response.Content.Text)
	}
	if ent.Timings.Wait < 0 || ent.Timings.Receive < 0 || ent.Timings.Connect != -1 {
		t.Errorf("timings = %+v", ent.Timings)
	}
}

func TestMiddlewareHijack(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		brw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nhi")
		brw.Flush()
	})
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hi" {
		t.Errorf("client got %q", body)
	}
	srv.Close()
	if c := onlyEntry(t, rec).Response.Comment; !strings.Contains(c, "hijacked") {
		t.Errorf("comment = %q", c)
	}
}

func TestMiddlewareSniffsContentType(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<!DOCTYPE html><p>hi")
	})
	serverGet(t, srv.URL)
	if got := onlyEntry(t, rec).Response.Content.MIMEType; got != "text/html; charset=utf-8" {
		t.Errorf("mimeType = %q", got)
	}
}

func TestMiddlewareDoesNotModifyRequest(t *testing.T) {
	rec := NewRecorder()
	inner := rec.Middleware(http.HandlerFunc(echo))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := r.Body
		inner.ServeHTTP(w, r)
		if r.Body != body {
			t.Error("middleware replaced the request body")
		}
	}))
	defer srv.Close()
	resp, err := http.Post(srv.URL, "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "payload" {
		t.Errorf("echo got %q", got)
	}
	if pd := onlyEntry(t, rec).Request.PostData; pd.Text != "payload" {
		t.Errorf("postData = %+v", pd)
	}
}

package harhar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// newTestServer serves h through a server-side recorder.
func newTestServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Recorder) {
	t.Helper()
	rec := NewRecorder()
	rec.Handler = h
	srv := httptest.NewServer(rec)
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
func TestServeHTTPEmptyHandler(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if resp := serverGet(t, srv.URL); resp.StatusCode != http.StatusOK {
		t.Errorf("client got status %d, want 200", resp.StatusCode)
	}
	if got := onlyEntry(t, rec).Response.StatusCode; got != http.StatusOK {
		t.Errorf("recorded status %d, want 200", got)
	}
}

// Multi-valued headers used to be collapsed to their last value.
func TestServeHTTPMultiValueHeaders(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
	})
	want := []string{"a=1", "b=2"}
	if got := serverGet(t, srv.URL).Header.Values("Set-Cookie"); !slices.Equal(got, want) {
		t.Errorf("client got Set-Cookie %q, want %q", got, want)
	}
	ent := onlyEntry(t, rec)
	if got := headerValues(ent.Response.Headers, "Set-Cookie"); !slices.Equal(got, want) {
		t.Errorf("recorded Set-Cookie %q, want %q", got, want)
	}
	if len(ent.Response.Cookies) != 2 {
		t.Errorf("recorded %d cookies, want 2", len(ent.Response.Cookies))
	}
}

func TestServeHTTPWriteHeaderSemantics(t *testing.T) {
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
	if ent.Response.StatusCode != http.StatusCreated {
		t.Errorf("recorded status %d, want 201", ent.Response.StatusCode)
	}
	if len(headerValues(ent.Response.Headers, "X-Late")) != 0 {
		t.Error("recorded a header set after WriteHeader")
	}
}

func TestServeHTTPTimingsAndServerIP(t *testing.T) {
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

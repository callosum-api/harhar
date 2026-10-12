package harhar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newTestClient returns a server running h and a client recording through rec.
func newTestClient(t *testing.T, h http.HandlerFunc) (*httptest.Server, *http.Client, *Recorder) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	rec := NewRecorder()
	return srv, &http.Client{Transport: rec}, rec
}

func mustDo(t *testing.T, cli *http.Client, req *http.Request) []byte {
	t.Helper()
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func onlyEntry(t *testing.T, rec *Recorder) Entry {
	t.Helper()
	if n := len(rec.HAR.Log.Entries); n != 1 {
		t.Fatalf("got %d entries, want 1", n)
	}
	return rec.HAR.Log.Entries[0]
}

func TestRoundTripGet(t *testing.T) {
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "hello")
	})

	req, _ := http.NewRequest("GET", srv.URL+"/path?a=1", nil)
	if got := string(mustDo(t, cli, req)); got != "hello" {
		t.Errorf("caller got body %q, want %q", got, "hello")
	}

	ent := onlyEntry(t, rec)
	if ent.Request.Method != "GET" || ent.Request.URL != srv.URL+"/path?a=1" {
		t.Errorf("request = %s %s", ent.Request.Method, ent.Request.URL)
	}
	if len(ent.Request.QueryParams) != 1 || ent.Request.QueryParams[0] != (NameValuePair{Name: "a", Value: "1"}) {
		t.Errorf("queryString = %+v", ent.Request.QueryParams)
	}
	if ent.Response.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d", ent.Response.StatusCode)
	}
	if ent.Response.Body.Content != "hello" || ent.Response.Body.MIMEType != "text/plain" {
		t.Errorf("content = %+v", ent.Response.Body)
	}
	if ent.Start == "" {
		t.Error("startedDateTime is empty")
	}
}

func TestWriteFile(t *testing.T) {
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	req, _ := http.NewRequest("GET", srv.URL, nil)
	mustDo(t, cli, req)

	name := filepath.Join(t.TempDir(), "out.har")
	n, err := rec.WriteFile(name)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(data) {
		t.Errorf("WriteFile returned %d, file has %d bytes", n, len(data))
	}
	var h HAR
	if err := json.Unmarshal(data, &h); err != nil {
		t.Fatal(err)
	}
	if len(h.Log.Entries) != 1 {
		t.Errorf("file has %d entries, want 1", len(h.Log.Entries))
	}
}

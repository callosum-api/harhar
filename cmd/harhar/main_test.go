package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/callosum-api/harhar"
)

func TestRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello")
	}))
	defer srv.Close()

	// An address that refuses connections.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + ln.Addr().String()
	ln.Close()

	out := filepath.Join(t.TempDir(), "out.har")
	var stderr bytes.Buffer
	// The failed URL comes first: the command should keep going.
	if code := run([]string{"-o", out, dead, srv.URL}, &stderr); code != 1 {
		t.Errorf("exit code %d, want 1 for a failed URL\n%s", code, &stderr)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var h harhar.HAR
	if err := json.Unmarshal(data, &h); err != nil {
		t.Fatal(err)
	}
	if len(h.Log.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(h.Log.Entries))
	}
	if failed, ok := h.Log.Entries[0], h.Log.Entries[1]; failed.Response.Status != 0 || ok.Response.Content.Text != "hello" {
		t.Errorf("entries = %+v, %+v", failed.Response, ok.Response)
	}
}

func TestRunUsage(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(nil, &stderr); code != 2 || !bytes.Contains(stderr.Bytes(), []byte("Usage")) {
		t.Errorf("exit code %d, output %q", code, &stderr)
	}
}

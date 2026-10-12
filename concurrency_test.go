package harhar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Requests used to be serialized by a lock held across the upstream call.
func TestRoundTripConcurrent(t *testing.T) {
	const n, delay = 5, 200 * time.Millisecond
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
	})

	start := time.Now()
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL, nil)
			mustDo(t, cli, req)
		}()
	}
	wg.Wait()

	if elapsed := time.Since(start); elapsed > n*delay/2 {
		t.Errorf("%d parallel requests took %v; they appear to be serialized", n, elapsed)
	}
	if len(rec.HAR.Log.Entries) != n {
		t.Errorf("got %d entries, want %d", len(rec.HAR.Log.Entries), n)
	}
}

// WriteFile must be safe to call while requests are recorded (run with -race).
func TestWriteFileWhileRecording(t *testing.T) {
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "body")
	})
	name := filepath.Join(t.TempDir(), "out.har")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 50 {
			req, _ := http.NewRequest("GET", srv.URL, nil)
			mustDo(t, cli, req)
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			if _, err := rec.WriteFile(name); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
}

func TestWriteFilePermissions(t *testing.T) {
	name := filepath.Join(t.TempDir(), "out.har")
	if err := os.WriteFile(name, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRecorder().WriteFile(name); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(name), ".*"))
	if len(leftovers) > 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

// A Recorder used as both the server handler and the client transport (a
// recording proxy) used to deadlock on its own lock.
func TestRecorderAsProxyNoDeadlock(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "upstream")
	}))
	defer upstream.Close()

	rec := NewRecorder()
	cli := &http.Client{Transport: rec}
	rec.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := cli.Get(upstream.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	})
	proxy := httptest.NewServer(rec)
	defer proxy.Close()

	done := make(chan []byte)
	go func() {
		req, _ := http.NewRequest("GET", proxy.URL, nil)
		done <- mustDo(t, http.DefaultClient, req)
	}()
	select {
	case body := <-done:
		if string(body) != "upstream" {
			t.Errorf("body = %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked")
	}
	if len(rec.HAR.Log.Entries) != 2 {
		t.Errorf("got %d entries, want 2", len(rec.HAR.Log.Entries))
	}
}

func TestRoundTripServerIP(t *testing.T) {
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	req, _ := http.NewRequest("GET", srv.URL, nil)
	mustDo(t, cli, req)
	if ip := onlyEntry(t, rec).ServerIP; ip != "127.0.0.1" {
		t.Errorf("serverIPAddress = %q, want 127.0.0.1", ip)
	}
}

// Package harhar records HTTP traffic from Go's net/http in the HTTP
// Archive (HAR) format.
//
// A Recorder collects entries from any number of clients and servers: use
// Recorder.Transport to record the requests an http.Client makes, and
// Recorder.Middleware to record the requests an http.Handler serves. Then
// save the log with Recorder.WriteFile or Recorder.WriteTo.
//
// HAR files contain the traffic as sent and received. Headers that usually
// carry credentials are redacted by default (see DefaultRedactedHeaders),
// but bodies, URLs and other headers are recorded as is, so treat HAR files
// as sensitive.
package harhar

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Recorder collects HAR entries. It is safe for concurrent use.
type Recorder struct {
	cfg config

	mu      sync.Mutex
	entries []Entry
	oldest  int // index of the oldest entry once entries is a full ring
}

// NewRecorder returns a Recorder configured by opts.
func NewRecorder(opts ...Option) *Recorder {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Recorder{cfg: cfg}
}

// add stores ent, subject to the WithMaxEntries limit, and passes it to the
// WithOnEntry callback.
func (r *Recorder) add(ent Entry) {
	r.mu.Lock()
	switch limit := r.cfg.maxEntries; {
	case limit < 0 || len(r.entries) < limit:
		r.entries = append(r.entries, ent)
	case limit > 0:
		// Full: overwrite the oldest entry.
		r.entries[r.oldest] = ent
		r.oldest = (r.oldest + 1) % limit
	}
	r.mu.Unlock()

	if r.cfg.onEntry != nil {
		r.cfg.onEntry(ent)
	}
}

// HAR returns a snapshot of the log recorded so far, oldest entry first.
// The entries share memory with the Recorder, so don't modify them.
func (r *Recorder) HAR() *HAR {
	h := NewHAR(r.cfg.creator.Name, r.cfg.creator.Version)
	r.mu.Lock()
	h.Log.Entries = append(h.Log.Entries, r.entries[r.oldest:]...)
	h.Log.Entries = append(h.Log.Entries, r.entries[:r.oldest]...)
	r.mu.Unlock()
	return h
}

// Reset discards all recorded entries.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.entries, r.oldest = nil, 0
	r.mu.Unlock()
}

// WriteTo writes the log recorded so far to w as JSON.
func (r *Recorder) WriteTo(w io.Writer) (int64, error) {
	data, err := json.Marshal(r.HAR())
	if err != nil {
		return 0, err
	}
	n, err := w.Write(data)
	return int64(n), err
}

// WriteFile writes the log recorded so far to filename and returns the
// number of bytes written.
//
// The file is replaced atomically and is readable only by the current user,
// since HAR data usually contains sensitive information.
func (r *Recorder) WriteFile(filename string) (int64, error) {
	data, err := json.Marshal(r.HAR())
	if err != nil {
		return 0, err
	}
	if err := writeFileAtomic(filename, data); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

// writeFileAtomic writes data to a 0600 temp file next to filename, then
// renames it into place so readers never see a partial file.
func writeFileAtomic(filename string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(filename), "."+filepath.Base(filename)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op once renamed

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}

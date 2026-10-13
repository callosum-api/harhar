// Package mock answers HTTP requests with the responses recorded in a HAR
// log, such as one written by a harhar.Recorder.
//
// A Mock can stand in for a server on either side of net/http: use
// Mock.Transport to answer an http.Client's requests without any network
// access, or Mock.Middleware to run a fake server, for example with
// httptest.NewServer.
//
// # Matching
//
// Requests are matched to entries by method, path and query, in any query
// order (see DefaultMatcher and WithMatcher). When several entries match,
// the mock prefers those for the request's own host, then those whose
// recorded body equals the request body. Repeated requests get the matching
// responses in the order they were recorded, and the last one repeats once
// all have been used, so recorded sequences such as polling play back as
// they happened.
//
// # Hostnames
//
// On the client side, requests must be for the host an entry was recorded
// from, unless WithHost maps that host to another one.
//
// On the server side, the mock answers for every host in the log: requests
// match regardless of their Host header, and URLs naming any recorded host
// are rewritten to point at the mock server, so a client that follows links
// or redirects keeps talking to the mock.
//
// Hostnames are rewritten where possible: in response headers, and in
// response bodies unless they are compressed or binary. Only URLs are
// rewritten, written as http://host, https://host or //host (slashes may
// be JSON-escaped), not bare hostnames. Cookie Domain attributes for
// rewritten hosts are dropped, so the cookies apply to the new host.
package mock

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/callosum-api/harhar"
	"github.com/callosum-api/harhar/internal/hostrewrite"
)

// ErrNoMatch is returned, wrapped, by the transport for a request that no
// entry matches when there is no fallback.
var ErrNoMatch = errors.New("harhar/mock: no recorded response")

// Mock answers requests with recorded responses. It is safe for concurrent
// use.
type Mock struct {
	cfg     config
	entries []entry
	hosts   []string              // recorded hosts
	urls    *hostrewrite.Rewriter // for URLs naming a recorded or mapped host

	mu     sync.Mutex
	served []int // how many times each entry has been served
}

type entry struct {
	harhar.Entry
	host string // normalized host of the recorded URL, "" if it has none
}

// New returns a Mock that answers with the entries in h. It does not modify
// h.
func New(h *harhar.HAR, opts ...Option) *Mock {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	m := &Mock{cfg: cfg}

	hosts := make(map[string]bool)
	for _, e := range h.Log.Entries {
		var host string
		if u, err := url.Parse(e.Request.URL); err == nil && u.Host != "" {
			host = hostrewrite.NormalizeHost(u.Scheme, u.Host)
			hosts[host] = true
		}
		m.entries = append(m.entries, entry{Entry: e, host: host})
	}
	m.served = make([]int, len(m.entries))
	m.hosts = slices.Sorted(maps.Keys(hosts))
	for host := range cfg.hosts {
		hosts[host] = true
	}
	m.urls = hostrewrite.New(slices.Collect(maps.Keys(hosts)))
	return m
}

// Load reads a HAR file and returns a Mock that answers with its entries.
func Load(filename string, opts ...Option) (*Mock, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var h harhar.HAR
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	return New(&h, opts...), nil
}

// Unused returns the entries that haven't been served since the Mock was
// created or Reset, in recorded order. Tests can use it to check that every
// recorded request was made. The entries share memory with the Mock, so
// don't modify them.
func (m *Mock) Unused() []harhar.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var unused []harhar.Entry
	for i, n := range m.served {
		if n == 0 {
			unused = append(unused, m.entries[i].Entry)
		}
	}
	return unused
}

// Reset forgets which entries have been served, so recorded sequences play
// back from the start.
func (m *Mock) Reset() {
	m.mu.Lock()
	clear(m.served)
	m.mu.Unlock()
}

// Transport returns an http.RoundTripper that answers requests with
// recorded responses. Requests that no entry matches are sent to fallback,
// or fail with ErrNoMatch if fallback is nil.
//
// A recorded request that failed with no response (status 0) fails again,
// with the recorded error text.
func (m *Mock) Transport(fallback http.RoundTripper) http.RoundTripper {
	return &transport{m: m, fallback: fallback}
}

type transport struct {
	m        *Mock
	fallback http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readBody(req.Body) // a RoundTripper must close the body
	if err != nil {
		return nil, err
	}
	scheme := req.URL.Scheme
	host := hostrewrite.NormalizeHost(scheme, req.URL.Host)

	// Match against a copy, so req is not modified.
	r := req.Clone(req.Context())
	i := t.m.lookup(r, host, body, true)
	if i < 0 {
		if t.fallback == nil {
			return nil, fmt.Errorf("%w for %s %s", ErrNoMatch, req.Method, req.URL.Redacted())
		}
		setBody(r, body)
		return t.fallback.RoundTrip(r)
	}

	e := &t.m.entries[i].Entry
	if !hasResponse(e) {
		return nil, recordedError(e)
	}
	header, respBody := t.m.response(e, t.m.targets(scheme, host, false))
	if req.Method == http.MethodHead {
		respBody = nil
	}
	if bodyAllowed(e.Response.Status) && req.Method != http.MethodHead {
		header.Set("Content-Length", strconv.Itoa(len(respBody)))
	}

	proto := e.Response.HTTPVersion
	major, minor, ok := http.ParseHTTPVersion(proto)
	if !ok {
		proto, major, minor = "HTTP/1.1", 1, 1
	}
	text := e.Response.StatusText
	if text == "" {
		text = http.StatusText(e.Response.Status)
	}
	resp := &http.Response{
		Status:        strconv.Itoa(e.Response.Status) + " " + text,
		StatusCode:    e.Response.Status,
		Proto:         proto,
		ProtoMajor:    major,
		ProtoMinor:    minor,
		Header:        header,
		Body:          http.NoBody,
		ContentLength: int64(len(respBody)),
		Request:       req,
	}
	if len(respBody) > 0 {
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}
	return resp, nil
}

// Middleware returns an http.Handler that answers requests with recorded
// responses. Requests that no entry matches are passed to next, or get a
// 404 Not Found if next is nil.
//
// A recorded request that failed with no response (status 0) is answered
// by aborting the connection, so the client sees an error too.
func (m *Mock) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := readBody(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		host := hostrewrite.NormalizeHost(scheme, r.Host)

		req := r.WithContext(r.Context())
		i := m.lookup(req, host, body, false)
		if i < 0 {
			if next == nil {
				msg := fmt.Sprintf("%v for %s %s", ErrNoMatch, r.Method, r.URL.RequestURI())
				http.Error(w, msg, http.StatusNotFound)
				return
			}
			setBody(req, body)
			next.ServeHTTP(w, req)
			return
		}

		e := &m.entries[i].Entry
		if !hasResponse(e) {
			panic(http.ErrAbortHandler)
		}
		header, respBody := m.response(e, m.targets(scheme, host, true))
		maps.Copy(w.Header(), header)
		w.WriteHeader(e.Response.Status)
		w.Write(respBody)
	})
}

// lookup returns the index of the entry to answer req with, or -1 if none
// matches, and counts it as served. host is the request's normalized host,
// and strictHost requires entries to be for that host. req must be a copy,
// since its body is replaced for each call to the matcher.
func (m *Mock) lookup(req *http.Request, host string, body []byte, strictHost bool) int {
	var cands []int
	for i := range m.entries {
		if strictHost && !m.sameHost(m.entries[i].host, host) {
			continue
		}
		setBody(req, body)
		if m.cfg.matcher(req, m.entries[i].Entry) {
			cands = append(cands, i)
		}
	}
	cands = narrow(cands, func(i int) bool {
		return m.sameHost(m.entries[i].host, host)
	})
	contentType := req.Header.Get("Content-Type")
	cands = narrow(cands, func(i int) bool {
		return bodyMatches(&m.entries[i].Request, contentType, body)
	})
	if len(cands) == 0 {
		return -1
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	pick := cands[len(cands)-1]
	for _, i := range cands {
		if m.served[i] == 0 {
			pick = i
			break
		}
	}
	m.served[pick]++
	return pick
}

// narrow returns the candidates for which keep is true, or all of them if
// it is true for none.
func narrow(cands []int, keep func(int) bool) []int {
	if len(cands) < 2 {
		return cands
	}
	kept := slices.DeleteFunc(slices.Clone(cands), func(i int) bool { return !keep(i) })
	if len(kept) == 0 {
		return cands
	}
	return kept
}

// sameHost reports whether a request for host can be answered with an
// entry recorded from recorded, directly or through WithHost.
func (m *Mock) sameHost(recorded, host string) bool {
	if recorded == "" {
		return false
	}
	if mapped, ok := m.cfg.hosts[recorded]; ok {
		return mapped == host
	}
	return recorded == host
}

// targets returns where URLs naming recorded hosts should point in the
// response to a request for scheme://host. With all set, every recorded
// host is redirected there; otherwise only those mapped by WithHost.
func (m *Mock) targets(scheme, host string, all bool) map[string]hostrewrite.Target {
	to := make(map[string]hostrewrite.Target)
	if all && host != "" {
		for _, h := range m.hosts {
			to[h] = hostrewrite.Target{Scheme: scheme, Host: host}
		}
	}
	for old, h := range m.cfg.hosts {
		t := hostrewrite.Target{Host: h}
		if h == host {
			t.Scheme = scheme
		}
		to[old] = t
	}
	return to
}

// hopHeaders are recorded headers that describe a connection or message
// framing rather than the response, so they are not replayed.
var hopHeaders = map[string]bool{
	"Connection":        true,
	"Content-Length":    true,
	"Keep-Alive":        true,
	"Proxy-Connection":  true,
	"Te":                true,
	"Trailer":           true,
	"Transfer-Encoding": true,
	"Upgrade":           true,
}

// response returns the headers and body recorded in e, with hosts
// rewritten as given by to.
func (m *Mock) response(e *harhar.Entry, to map[string]hostrewrite.Target) (http.Header, []byte) {
	header := make(http.Header)
	for _, h := range e.Response.Headers {
		name := http.CanonicalHeaderKey(h.Name)
		if strings.HasPrefix(name, ":") || hopHeaders[name] {
			continue // HTTP/2 pseudo-headers appear in browser HARs
		}
		value := m.urls.Rewrite(h.Value, to)
		if name == "Set-Cookie" {
			value = hostrewrite.DropCookieDomain(value, to)
		}
		header.Add(name, value)
	}

	body, rewritable := decodeContent(e.Response.Content)
	if ce := header.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
		rewritable = false
	}
	if rewritable {
		body = []byte(m.urls.Rewrite(string(body), to))
	}
	return header, body
}

// hasResponse reports whether e recorded a response, as opposed to a
// request that failed.
func hasResponse(e *harhar.Entry) bool {
	return e.Response.Status >= 100 && e.Response.Status <= 999
}

func recordedError(e *harhar.Entry) error {
	if e.Response.Comment == "" {
		return errors.New("harhar/mock: recorded request failed")
	}
	return errors.New("harhar/mock: recorded request failed: " + e.Response.Comment)
}

// bodyAllowed reports whether a response with status may have a body.
func bodyAllowed(status int) bool {
	return status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}

// readBody reads and closes rc, which may be nil.
func readBody(rc io.ReadCloser) ([]byte, error) {
	if rc == nil {
		return nil, nil
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// setBody gives r a fresh body reading from body.
func setBody(r *http.Request, body []byte) {
	if len(body) == 0 {
		r.Body, r.GetBody = http.NoBody, nil
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

// decodeContent returns the body recorded in c, and whether it is text
// that hosts can be rewritten in.
func decodeContent(c harhar.Content) ([]byte, bool) {
	if c.Encoding == "base64" {
		if b, err := base64.StdEncoding.DecodeString(c.Text); err == nil {
			return b, false
		}
	}
	return []byte(c.Text), true
}

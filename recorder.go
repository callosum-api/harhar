// Package harhar provides a minimal set of methods and structs to enable
// HAR logging in a go net/http-based application.
package harhar

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Recorder embeds an upstream RoundTripper and wraps its methods to perform transparent HAR
// logging for every request and response
type Recorder struct {
	mu           sync.Mutex
	RoundTripper http.RoundTripper `json:"-"`
	Handler      http.Handler      `json:"-"`

	DisableHTTP2 func(bool)

	HAR *HAR
}

// NewRecorder returns a new Recorder object that fulfills the http.RoundTripper interface
func NewRecorder() *Recorder {
	h := NewHAR(filepath.Base(os.Args[0]))

	// copy of DefaultTransport but with the
	// ability to disable HTTP/2 as needed
	tport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,

		// if nil, then HTTP/2 is enabled
		// if non-nil, then HTTP/2 is disabled
		//TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}

	return &Recorder{
		RoundTripper: tport,
		Handler:      http.DefaultServeMux,
		HAR:          h,
		DisableHTTP2: func(disable2 bool) {
			if disable2 {
				if tport.TLSNextProto == nil {
					tport.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
				}
			} else {
				tport.TLSNextProto = nil
			}
		},
	}
}

// WriteFile writes the HAR log format to the filename given, then returns the
// number of bytes. It is safe to call while requests are being recorded.
//
// The file is replaced atomically and is readable only by the current user,
// since HAR data usually contains credentials.
func (c *Recorder) WriteFile(filename string) (int, error) {
	// Entries are only ever appended, so a shallow copy is a stable snapshot.
	c.mu.Lock()
	h := *c.HAR
	c.mu.Unlock()

	data, err := json.Marshal(&h)
	if err != nil {
		return 0, err
	}
	if err := writeFileAtomic(filename, data); err != nil {
		return 0, err
	}
	return len(data), nil
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

func (c *Recorder) addEntry(ent Entry) {
	c.mu.Lock()
	c.HAR.Log.Entries = append(c.HAR.Log.Entries, ent)
	c.mu.Unlock()
}

// RoundTrip implements http.RoundTripper
func (c *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readBody(req.Body)
	if err != nil {
		return nil, err
	}
	ent := Entry{Request: makeRequest(req, body)}

	// Send a copy so the caller's request is not modified.
	rt := &requestTrace{}
	out := req.WithContext(httptrace.WithClientTrace(req.Context(), rt.clientTrace()))
	if req.Body != nil && req.Body != http.NoBody {
		out.Body = replayBody(body, nil)
		out.GetBody = func() (io.ReadCloser, error) { return replayBody(body, nil), nil }
	}

	startTime := time.Now()
	resp, err := c.RoundTripper.RoundTrip(out)
	if err != nil {
		// Record the failure too, with status 0 as browsers do.
		ent.Response = failedResponse(err)
	} else {
		body, readErr := readBody(resp.Body)
		// The caller sees the same data, and the same error, that we did.
		resp.Body = replayBody(body, readErr)
		ent.Response = makeResponse(resp, body)
		if readErr != nil {
			ent.Response.Comment = "error reading body: " + readErr.Error()
		}
	}
	endTime := time.Now()
	ent.Timings, ent.ServerIP = rt.timings(endTime)
	ent.Time = int(endTime.Sub(startTime).Milliseconds())
	ent.Start = startTime.Format(time.RFC3339Nano)

	c.addEntry(ent)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// requestTrace collects connection timestamps for a single request. The
// httptrace hooks may run concurrently, and even after RoundTrip returns, so
// every field is guarded by mu.
type requestTrace struct {
	mu sync.Mutex

	getConn, gotConn          time.Time
	dnsStart, dnsDone         time.Time
	connectStart, connectDone time.Time
	tlsStart, tlsDone         time.Time
	wroteRequest, firstByte   time.Time
	remoteAddr                net.Addr
}

// mark records the current time in *t unless it was already set, so that
// repeated hooks (e.g. parallel dials) keep the earliest time.
func (rt *requestTrace) mark(t *time.Time) {
	rt.mu.Lock()
	if t.IsZero() {
		*t = time.Now()
	}
	rt.mu.Unlock()
}

func (rt *requestTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) { rt.mark(&rt.getConn) },
		GotConn: func(info httptrace.GotConnInfo) {
			rt.mark(&rt.gotConn)
			if info.Conn != nil {
				rt.mu.Lock()
				rt.remoteAddr = info.Conn.RemoteAddr()
				rt.mu.Unlock()
			}
		},
		DNSStart:     func(httptrace.DNSStartInfo) { rt.mark(&rt.dnsStart) },
		DNSDone:      func(httptrace.DNSDoneInfo) { rt.mark(&rt.dnsDone) },
		ConnectStart: func(_, _ string) { rt.mark(&rt.connectStart) },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				rt.mark(&rt.connectDone)
			}
		},
		TLSHandshakeStart: func() { rt.mark(&rt.tlsStart) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				rt.mark(&rt.tlsDone)
			}
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { rt.mark(&rt.wroteRequest) },
		GotFirstResponseByte: func() { rt.mark(&rt.firstByte) },
	}
}

// timings converts the collected timestamps into HAR timings, with end as the
// time the response body was fully read. It also returns the server IP.
func (rt *requestTrace) timings(end time.Time) (Timings, string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	t := Timings{
		DNS:     msBetween(rt.dnsStart, rt.dnsDone),
		SSL:     msBetween(rt.tlsStart, rt.tlsDone),
		Send:    msBetween(rt.gotConn, rt.wroteRequest),
		Wait:    msBetween(rt.wroteRequest, rt.firstByte),
		Receive: msBetween(rt.firstByte, end),
	}
	// In HAR, connect includes the TLS handshake.
	connectEnd := rt.connectDone
	if rt.tlsDone.After(connectEnd) {
		connectEnd = rt.tlsDone
	}
	t.Connect = msBetween(rt.connectStart, connectEnd)
	// Blocked is time spent waiting for a connection, excluding setting one up.
	t.Blocked = max(msBetween(rt.getConn, rt.gotConn)-t.DNS-t.Connect, 0)

	var ip string
	if rt.remoteAddr != nil {
		ip = addrIP(rt.remoteAddr)
	}
	return t, ip
}

// addrIP returns the host part of a network address, e.g. "127.0.0.1".
func addrIP(addr net.Addr) string {
	s := addr.String()
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	return s
}

// msBetween returns the milliseconds from start to end, or 0 if either is unset.
func msBetween(start, end time.Time) int {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return int(end.Sub(start).Milliseconds())
}

// readBody reads and closes rc. A nil rc or http.NoBody yields no data.
func readBody(rc io.ReadCloser) ([]byte, error) {
	if rc == nil || rc == http.NoBody {
		return nil, nil
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// replayBody returns a body that yields data and then err, or io.EOF if err
// is nil.
func replayBody(data []byte, err error) io.ReadCloser {
	if err != nil {
		return io.NopCloser(io.MultiReader(bytes.NewReader(data), errReader{err}))
	}
	if len(data) == 0 {
		return http.NoBody
	}
	return io.NopCloser(bytes.NewReader(data))
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// makeRequest converts an http.Request, whose body has already been read
// into body, to a harhar.Request.
func makeRequest(hr *http.Request, body []byte) Request {
	r := Request{
		Method:      hr.Method,
		URL:         requestURL(hr),
		HTTPVersion: hr.Proto,
		Cookies:     makeCookies(hr.Cookies()),
		Headers:     makePairs(hr.Header),
		QueryParams: parseQuery(hr.URL.RawQuery),
		HeadersSize: headersSize(hr.Header),
		BodySize:    len(body),
	}
	if r.QueryParams == nil {
		r.QueryParams = []NameValuePair{}
	}
	if len(body) > 0 {
		r.Body = makePostData(hr.Header.Get("Content-Type"), body)
	}
	return r
}

// requestURL returns the absolute URL of hr, without its fragment and with
// any password redacted.
func requestURL(hr *http.Request) string {
	u := *hr.URL
	u.Fragment, u.RawFragment = "", ""
	if !u.IsAbs() {
		// Server-side requests usually carry only the path and query.
		u.Scheme = "http"
		if hr.TLS != nil {
			u.Scheme = "https"
		}
		u.Host = hr.Host
	}
	return u.Redacted()
}

// makePostData converts a request body to HAR postData, parsing form
// parameters when the content type is a form encoding.
func makePostData(contentType string, body []byte) BodyType {
	pd := BodyType{MIMEType: contentType}
	if pd.MIMEType == "" {
		// default per RFC 9110 section 8.3
		pd.MIMEType = "application/octet-stream"
	}

	if mediaType, params, err := mime.ParseMediaType(pd.MIMEType); err == nil {
		switch mediaType {
		case "application/x-www-form-urlencoded":
			for _, p := range parseQuery(string(body)) {
				pd.Params = append(pd.Params, PostNameValuePair{Name: p.Name, Value: p.Value})
			}
		case "multipart/form-data":
			// On a malformed body, fall back to recording the raw text.
			pd.Params, _ = multipartParams(body, params["boundary"])
		}
	}

	// HAR's params and text are mutually exclusive.
	if pd.Params == nil {
		pd.Content = string(body)
	}
	return pd
}

// multipartParams parses a multipart/form-data body into HAR params, in the
// order they were sent.
func multipartParams(body []byte, boundary string) ([]PostNameValuePair, error) {
	if boundary == "" {
		return nil, errors.New("multipart body has no boundary")
	}
	var params []PostNameValuePair
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return params, nil
		}
		if err != nil {
			return nil, err
		}
		value, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		params = append(params, PostNameValuePair{
			Name:        part.FormName(),
			Value:       string(value),
			FileName:    part.FileName(),
			ContentType: part.Header.Get("Content-Type"),
		})
	}
}

// parseQuery parses a URL-encoded query string, keeping the original order
// and any repeated names. Undecodable names or values are kept as sent.
func parseQuery(query string) []NameValuePair {
	var pairs []NameValuePair
	for query != "" {
		var pair string
		pair, query, _ = strings.Cut(query, "&")
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		if s, err := url.QueryUnescape(name); err == nil {
			name = s
		}
		if s, err := url.QueryUnescape(value); err == nil {
			value = s
		}
		pairs = append(pairs, NameValuePair{Name: name, Value: value})
	}
	return pairs
}

// makePairs converts headers to HAR name/value pairs, sorted by name so the
// output is deterministic.
func makePairs(h http.Header) []NameValuePair {
	pairs := make([]NameValuePair, 0, len(h))
	for _, name := range slices.Sorted(maps.Keys(h)) {
		for _, val := range h[name] {
			pairs = append(pairs, NameValuePair{Name: name, Value: val})
		}
	}
	return pairs
}

func makeCookies(cookies []*http.Cookie) []Cookie {
	out := make([]Cookie, 0, len(cookies))
	for _, c := range cookies {
		nc := Cookie{
			Name:     c.Name,
			Path:     c.Path,
			Value:    c.Value,
			Domain:   c.Domain,
			HTTPOnly: c.HttpOnly,
			Secure:   c.Secure,
		}
		if !c.Expires.IsZero() {
			nc.Expires = c.Expires.Format(time.RFC3339Nano)
		}
		out = append(out, nc)
	}
	return out
}

// headersSize approximates the size of the header block in bytes.
func headersSize(h http.Header) int {
	var buf bytes.Buffer
	h.Write(&buf)
	return buf.Len() + 4 // incl. CRLF CRLF
}

// makeResponse converts an http.Response, whose body has already been read
// into body, to a harhar.Response.
func makeResponse(hr *http.Response, body []byte) Response {
	r := Response{
		StatusCode:  hr.StatusCode,
		StatusText:  statusText(hr),
		HTTPVersion: hr.Proto,
		Cookies:     makeCookies(hr.Cookies()),
		Headers:     makePairs(hr.Header),
		HeadersSize: headersSize(hr.Header),
		BodySize:    len(body),
	}
	if rurl, err := hr.Location(); err == nil {
		r.RedirectURL = rurl.String()
	}

	r.Body.Size = len(body)
	r.Body.MIMEType = hr.Header.Get("Content-Type")
	if r.Body.MIMEType == "" {
		// default per RFC 9110 section 8.3
		r.Body.MIMEType = "application/octet-stream"
	}

	// net/http transparently decompresses gzip responses it requested, in
	// which case the size on the wire (and so r.Body.Compression) is unknown.
	if hr.Uncompressed {
		r.BodySize = -1
	}

	// Content in other charsets is not transcoded to UTF-8 as the spec asks,
	// but anything that isn't valid UTF-8 is base64-encoded so that no data
	// is lost in the JSON output.
	if utf8.Valid(body) {
		r.Body.Content = string(body)
	} else {
		r.Body.Content = base64.StdEncoding.EncodeToString(body)
		r.Body.Encoding = "base64"
	}
	return r
}

// statusText returns the reason phrase the server sent, falling back to the
// standard text for the code.
func statusText(hr *http.Response) string {
	if text, ok := strings.CutPrefix(hr.Status, strconv.Itoa(hr.StatusCode)+" "); ok {
		return text
	}
	return http.StatusText(hr.StatusCode)
}

// failedResponse is the HAR response recorded for a request that got no
// response at all.
func failedResponse(err error) Response {
	return Response{
		Cookies:     []Cookie{},
		Headers:     []NameValuePair{},
		Body:        BodyResponseType{MIMEType: "x-unknown"},
		HeadersSize: -1,
		BodySize:    -1,
		Comment:     err.Error(),
	}
}

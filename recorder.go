// Package harhar provides a minimal set of methods and structs to enable
// HAR logging in a go net/http-based application.
package harhar

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync"
	"time"
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
	h := NewHAR(os.Args[0])

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
	var err error
	ent := Entry{}
	ent.Request, err = makeRequest(req)
	if err != nil {
		return nil, err
	}

	rt := &requestTrace{}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), rt.clientTrace()))

	startTime := time.Now()
	resp, err := c.RoundTripper.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	ent.Response, err = makeResponse(resp)
	endTime := time.Now()
	ent.Timings, ent.ServerIP = rt.timings(endTime)
	ent.Time = int(endTime.Sub(startTime).Milliseconds())
	ent.Start = startTime.Format(time.RFC3339Nano)

	c.addEntry(ent)
	return resp, err
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
		ip = rt.remoteAddr.String()
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
	}
	return t, ip
}

// msBetween returns the milliseconds from start to end, or 0 if either is unset.
func msBetween(start, end time.Time) int {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return int(end.Sub(start).Milliseconds())
}

// convert an http.Request to a harhar.Request
func makeRequest(hr *http.Request) (Request, error) {
	r := Request{
		Method:      hr.Method,
		URL:         hr.URL.String(),
		HTTPVersion: hr.Proto,
		HeadersSize: -1,
		BodySize:    -1,
	}

	h2 := hr.Header.Clone()
	buf := &bytes.Buffer{}
	h2.Write(buf)
	r.HeadersSize = buf.Len() + 4 // incl. CRLF CRLF

	// parse out headers
	r.Headers = make([]NameValuePair, 0, len(hr.Header))
	for name, vals := range hr.Header {
		for _, val := range vals {
			r.Headers = append(r.Headers, NameValuePair{Name: name, Value: val})
		}
	}

	// parse out cookies
	r.Cookies = make([]Cookie, 0, len(hr.Cookies()))
	for _, c := range hr.Cookies() {
		nc := Cookie{
			Name:     c.Name,
			Path:     c.Path,
			Value:    c.Value,
			Domain:   c.Domain,
			Expires:  c.Expires.Format(time.RFC3339Nano),
			HTTPOnly: c.HttpOnly,
			Secure:   c.Secure,
		}
		r.Cookies = append(r.Cookies, nc)
	}

	// parse query params
	qp := hr.URL.Query()
	r.QueryParams = make([]NameValuePair, 0, len(qp))
	for name, vals := range qp {
		for _, val := range vals {
			r.QueryParams = append(r.QueryParams, NameValuePair{Name: name, Value: val})
		}
	}

	if hr.Body == nil {
		r.BodySize = 0
		return r, nil
	}

	// read in all the data and replace the ReadCloser
	bodyData, err := io.ReadAll(hr.Body)
	if err != nil {
		return r, err
	}
	hr.Body.Close()
	bodbuf := bytes.NewReader(bodyData)
	hr.Body = io.NopCloser(bodbuf)

	r.BodySize = len(bodyData)
	r.Body.MIMEType = hr.Header.Get("Content-Type")
	if r.Body.MIMEType == "" {
		// default per RFC2616
		r.Body.MIMEType = "application/octet-stream"
	}
	switch r.Body.MIMEType {
	case "form-data", "multipart/form-data":
		err = hr.ParseMultipartForm(32 << 20) // 32 MB
		if err != nil {
			return r, err
		}
		bodbuf.Seek(0, io.SeekStart)
		for key, fheads := range hr.MultipartForm.File {
			for _, fh := range fheads {
				fhandle, err := fh.Open()
				if err != nil {
					return r, err
				}
				fileContents, err := io.ReadAll(fhandle)
				fhandle.Close()
				if err != nil {
					return r, err
				}

				r.Body.Params = append(r.Body.Params, PostNameValuePair{
					Name:        key,
					Value:       string(fileContents),
					FileName:    fh.Filename,
					ContentType: fh.Header.Get("Content-Type"),
				})
			}
		}
		for key, vals := range hr.MultipartForm.Value {
			for _, val := range vals {
				r.Body.Params = append(r.Body.Params, PostNameValuePair{
					Name:  key,
					Value: val,
				})
			}
		}

	case "application/x-www-form-urlencoded":
		err = hr.ParseForm()
		if err != nil {
			return r, err
		}
		bodbuf.Seek(0, io.SeekStart)
		for key, vals := range hr.PostForm {
			for _, val := range vals {
				r.Body.Params = append(r.Body.Params, PostNameValuePair{
					Name:  key,
					Value: val,
				})
			}
		}

	default:
		r.Body.Content = string(bodyData)
	}

	return r, nil
}

// convert an http.Response to a harhar.Response
func makeResponse(hr *http.Response) (Response, error) {
	r := Response{
		StatusCode:  hr.StatusCode,
		StatusText:  http.StatusText(hr.StatusCode),
		HTTPVersion: hr.Proto,
		HeadersSize: -1,
		BodySize:    -1,
	}

	h2 := hr.Header.Clone()
	buf := &bytes.Buffer{}
	h2.Write(buf)
	r.HeadersSize = buf.Len() + 4 // incl. CRLF CRLF

	// parse out headers
	r.Headers = make([]NameValuePair, 0, len(hr.Header))
	for name, vals := range hr.Header {
		for _, val := range vals {
			r.Headers = append(r.Headers, NameValuePair{Name: name, Value: val})
		}
	}
	rurl, err := hr.Location()
	if err == nil {
		r.RedirectURL = rurl.String()
	}

	// parse out cookies
	r.Cookies = make([]Cookie, 0, len(hr.Cookies()))
	for _, c := range hr.Cookies() {
		nc := Cookie{
			Name:     c.Name,
			Path:     c.Path,
			Value:    c.Value,
			Domain:   c.Domain,
			Expires:  c.Expires.Format(time.RFC3339Nano),
			HTTPOnly: c.HttpOnly,
			Secure:   c.Secure,
		}
		r.Cookies = append(r.Cookies, nc)
	}

	// FIXME: net/http transparently decompresses content,
	// so r.Body.Size and r.Body.Compression are not true to the server's response
	// also, if the response is not utf-8, then r.Body.Content and r.Body.Encoding
	// are not properly handled (spec says to decode anything into UTF-8)
	//
	// see hr.Uncompressed for next steps

	// read in all the data and replace the ReadCloser
	bodyData, err := io.ReadAll(hr.Body)
	if err != nil {
		return r, err
	}
	hr.Body.Close()
	hr.Body = io.NopCloser(bytes.NewReader(bodyData))
	r.Body.Content = string(bodyData)
	r.Body.Compression = 0
	r.Body.Size = len(bodyData)
	r.BodySize = r.Body.Size

	r.Body.MIMEType = hr.Header.Get("Content-Type")
	if r.Body.MIMEType == "" {
		// default per RFC2616
		r.Body.MIMEType = "application/octet-stream"
	}

	return r, nil
}

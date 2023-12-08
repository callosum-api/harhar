// Package harhar provides a minimal set of methods and structs to enable
// HAR logging in a go net/http-based application.
package harhar

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"sync"
	"time"
)

// Client embeds an upstream RoundTripper and wraps its methods to perform transparent HAR
// logging for every request and response
type Recorder struct {
	mu           sync.Mutex
	RoundTripper http.RoundTripper `json:"-"`
	Handler      http.Handler      `json:"-"`

	HAR *HAR
}

// NewRecorder returns a new Recorder object that fulfills the http.RoundTripper interface
func NewRecorder() *Recorder {
	h := NewHAR(os.Args[0])

	return &Recorder{
		RoundTripper: http.DefaultTransport,
		Handler:      http.DefaultServeMux,
		HAR:          h,
	}
}

// WriteLog writes the HAR log format to the filename given, then returns the
// number of bytes.
func (c *Recorder) WriteFile(filename string) (int, error) {
	data, err := json.Marshal(c.HAR)
	if err != nil {
		return 0, err
	}
	return len(data), os.WriteFile(filename, data, 0644)
}

// RoundTrip implements http.RoundTripper
func (c *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	// http.RoundTripper must be safe for concurrent use
	c.mu.Lock()
	defer c.mu.Unlock()

	var err error
	ent := Entry{}
	ent.Request, err = makeRequest(req)
	if err != nil {
		return nil, err
	}

	// if we re-use a connection many trace hooks don't fire, so
	// set a start time for everything
	now := time.Now()
	dnsStart := now
	tlsStart := now
	connWaitStart := now
	connStart := now
	sendStart := now
	waitStart := now
	respStart := now

	trace := &httptrace.ClientTrace{
		GetConn: func(hostPort string) {
			connWaitStart = time.Now()
		},
		GotConn: func(connInfo httptrace.GotConnInfo) {
			ent.Timings.Blocked = int(time.Since(connWaitStart).Milliseconds())
		},

		DNSStart: func(dnsInfo httptrace.DNSStartInfo) {
			dnsStart = time.Now()
		},
		DNSDone: func(dnsInfo httptrace.DNSDoneInfo) {
			ent.Timings.DNS = int(time.Since(dnsStart).Milliseconds())
			ent.ServerIP = dnsInfo.Addrs[0].String()
		},

		ConnectStart: func(network, addr string) {
			connStart = time.Now()
		},
		ConnectDone: func(network, addr string, err error) {
			ent.Timings.Connect = int(time.Since(connStart).Milliseconds())
			sendStart = time.Now()
		},

		TLSHandshakeStart: func() {
			tlsStart = time.Now()
		},
		TLSHandshakeDone: func(connState tls.ConnectionState, err error) {
			ent.Timings.SSL = int(time.Since(tlsStart).Milliseconds())
		},

		WroteRequest: func(info httptrace.WroteRequestInfo) {
			ent.Timings.Send = int(time.Since(sendStart).Milliseconds())
			waitStart = time.Now()
		},
		GotFirstResponseByte: func() {
			ent.Timings.Wait = int(time.Since(waitStart).Milliseconds())
			respStart = time.Now()
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	startTime := time.Now()
	resp, err := c.RoundTripper.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	ent.Response, err = makeResponse(resp)
	ent.Timings.Receive = int(time.Since(respStart).Milliseconds())
	ent.Time = int(time.Since(startTime).Milliseconds())
	ent.Start = startTime.Format(time.RFC3339Nano)

	c.HAR.Log.Entries = append(c.HAR.Log.Entries, ent)
	return resp, err
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

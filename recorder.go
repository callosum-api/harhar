/*
The MIT License (MIT)

Copyright (c) 2014 Stridatum LLC <code@stridatum.com>

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/

// Package harhar provides a minimal set of methods and structs to enable
// HAR logging in a go net/http-based application.
package harhar

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"time"
)

// Client embeds an upstream RoundTripper and wraps its methods to perform transparent HAR
// logging for every request and response
type Recorder struct {
	http.RoundTripper `json:"-"`
	HAR               *HAR
}

// NewRecorder returns a new Recorder object that fulfills the http.RoundTripper interface
func NewRecorder() *Recorder {
	h := NewHAR()
	h.Log.Creator.Name = os.Args[0]

	return &Recorder{
		RoundTripper: http.DefaultTransport,
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
	return len(data), ioutil.WriteFile(filename, data, 0644)
}

func (c *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var err error
	ent := Entry{}
	ent.Request, err = makeRequest(req)
	if err != nil {
		return nil, err
	}
	ent.Cache = make(map[string]string)

	startTime := time.Now()
	resp, err := c.RoundTripper.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	ent.Timings.Wait = int(time.Now().Sub(startTime).Seconds() * 1000.0)
	ent.Time = ent.Timings.Wait

	// TODO: implement send and receive
	ent.Timings.Send = -1
	ent.Timings.Receive = -1

	ent.Start = startTime.Format(time.RFC3339Nano)
	ent.Response, err = makeResponse(resp)

	c.HAR.Log.Entries = append(c.HAR.Log.Entries, ent)
	return resp, err
}

// convert an http.Request to a harhar.Request
func makeRequest(hr *http.Request) (Request, error) {
	r := Request{
		Method:      hr.Method,
		URL:         hr.URL.String(),
		HttpVersion: hr.Proto,
		HeadersSize: -1,
		BodySize:    -1,
	}

	// parse out headers
	r.Headers = make([]NameValuePair, 0, len(hr.Header))
	for name, vals := range hr.Header {
		for _, val := range vals {
			r.Headers = append(r.Headers, NameValuePair{name, val})
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
			HttpOnly: c.HttpOnly,
			Secure:   c.Secure,
		}
		r.Cookies = append(r.Cookies, nc)
	}

	// parse query params
	qp := hr.URL.Query()
	r.QueryParams = make([]NameValuePair, 0, len(qp))
	for name, vals := range qp {
		for _, val := range vals {
			r.QueryParams = append(r.QueryParams, NameValuePair{name, val})
		}
	}

	if hr.Body == nil {
		return r, nil
	}

	// read in all the data and replace the ReadCloser
	bodyData, err := ioutil.ReadAll(hr.Body)
	if err != nil {
		return r, err
	}
	hr.Body.Close()
	hr.Body = ioutil.NopCloser(bytes.NewReader(bodyData))

	r.Body.Content = string(bodyData)
	r.Body.MIMEType = hr.Header.Get("Content-Type")
	if r.Body.MIMEType == "" {
		// default per RFC2616
		r.Body.MIMEType = "application/octet-stream"
	}

	return r, nil
}

// convert an http.Response to a harhar.Response
func makeResponse(hr *http.Response) (Response, error) {
	r := Response{
		StatusCode:  hr.StatusCode,
		StatusText:  http.StatusText(hr.StatusCode),
		HttpVersion: hr.Proto,
		HeadersSize: -1,
		BodySize:    -1,
	}

	// parse out headers
	r.Headers = make([]NameValuePair, 0, len(hr.Header))
	for name, vals := range hr.Header {
		for _, val := range vals {
			r.Headers = append(r.Headers, NameValuePair{name, val})
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
			HttpOnly: c.HttpOnly,
			Secure:   c.Secure,
		}
		r.Cookies = append(r.Cookies, nc)
	}

	// read in all the data and replace the ReadCloser
	bodyData, err := ioutil.ReadAll(hr.Body)
	if err != nil {
		return r, err
	}
	hr.Body.Close()
	hr.Body = ioutil.NopCloser(bytes.NewReader(bodyData))
	r.Body.Content = string(bodyData)
	r.Body.Size = len(bodyData)

	r.Body.MIMEType = hr.Header.Get("Content-Type")
	if r.Body.MIMEType == "" {
		// default per RFC2616
		r.Body.MIMEType = "application/octet-stream"
	}

	return r, nil
}

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

package harhar

import "time"

// This file contains the struct definitinos for the various components of a
// HAR logfile. It omits many optional properties for brevity, and because
// harhar is generally only useful in a server (non-browser) application mode.
//
// W3C Spec:
//   https://dvcs.w3.org/hg/webperf/raw-file/tip/specs/HAR/Overview.html

type HAR struct {
	Log Log `json:"log"`
}

func NewHAR() *HAR {
	v := time.Now().Format("20060102150405")

	return &HAR{
		Log: Log{
			Version: v,
			Creator: Creator{
				Version: v,
			},
		},
	}
	// add some reasonable defaults
	// r.Creator.Name = os.Args[0]
	// r.Creator.Version = nowVersion
}

// Creator describes the source of the logged requests/responses.
type Creator struct {
	// Name defaults to the name of the program (os.Args[0])
	Name string `json:"name"`

	// Version defaults to the current time (formatted as "20060102150405")
	Version string `json:"version"`
}

type Log struct {
	Creator Creator `json:"creator"`

	// Version defaults to the current time (formatted as "20060102150405")
	Version string `json:"version"`

	// Comment can be added to the log to describe the particulars of this data.
	Comment string `json:"comment,omitempty"`

	// Entries contains all of the Request and Response details that passed
	// through this Client.
	Entries []Entry `json:"entries"`
}

type Entry struct {
	Request  Request  `json:"request"`
	Response Response `json:"response"`

	Start string `json:"startedDateTime"` // ISO8601 time

	// Total time in milliseconds, Time=SUM(Timings.*)
	Time    int `json:"time"`
	Timings struct {
		Send    int `json:"send"`
		Wait    int `json:"wait"`
		Receive int `json:"receive"`
	} `json:"timings"`

	// always empty
	Cache map[string]string `json:"cache"`
}

type Request struct {
	Method      string          `json:"method"` // in caps, GET/POST/etc
	URL         string          `json:"url"`
	HttpVersion string          `json:"httpVersion"` // ex "HTTP/1.1"
	Headers     []NameValuePair `json:"headers"`
	Cookies     []Cookie        `json:"cookies"`
	QueryParams []NameValuePair `json:"queryString"`

	Body struct {
		MIMEType string `json:"mimeType"`
		Content  string `json:"text"`
	} `json:"postData"`

	// always -1, too lazy
	HeadersSize int `json:"headersSize"`
	BodySize    int `json:"bodySize"`
}

type Response struct {
	StatusCode  int             `json:"status"`      // 200
	StatusText  string          `json:"statusText"`  // "OK"
	HttpVersion string          `json:"httpVersion"` // ex "HTTP/1.1"
	RedirectURL string          `json:"redirectURL"`
	Headers     []NameValuePair `json:"headers"`
	Cookies     []Cookie        `json:"cookies"`

	Body struct {
		Size     int    `json:"size"`
		MIMEType string `json:"mimeType"`
		Content  string `json:"text"`
	} `json:"content"`

	// always -1, too lazy
	HeadersSize int `json:"headersSize"`
	BodySize    int `json:"bodySize"`
}

type NameValuePair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Expires  string `json:"expires,omitempty"` // ISO8601 time
	Secure   bool   `json:"secure"`
	HttpOnly bool   `json:"httpOnly"`
}

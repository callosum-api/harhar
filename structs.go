/*
The MIT License (MIT)

Copyright (c) 2014 Jeremy Jay

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

// This file contains the struct definitions for the various components of a
// HAR logfile. It omits many optional properties for brevity, and because
// harhar is generally only useful in a server (non-browser) application mode.
//
// W3C Spec:
//   https://w3c.github.io/web-performance/specs/HAR/Overview.html

type HAR struct {
	Log Log `json:"log"`
}

// NewHAR creates a new HTTP Archive document with the provided Creator Name.
// The recommended invocation is NewHAR(os.Args[0]).
func NewHAR(creatorName string) *HAR {
	v := time.Now().Format("20060102150405")

	return &HAR{
		Log: Log{
			Version: v,
			Creator: Creator{
				Name:    creatorName,
				Version: v,
			},
		},
	}
}

// Creator describes the source of the logged requests/responses.
type Creator struct {
	// Name of the HTTP creator source.
	Name string `json:"name"`

	// Version of the HTTP creator source.
	Version string `json:"version"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Log represent a set of HTTP Request/Response Entries.
type Log struct {
	// Version of the log, defaults to the current time (formatted as "20060102150405")
	Version string `json:"version"`

	// Creator of this set of Log entries.
	Creator Creator `json:"creator"`

	// Browser information that produced this set of Log entries.
	Browser *Creator `json:"browser,omitempty"`

	// Pages contain information about request groupings, such as a page loaded by a web browser.
	Pages []Page `json:"pages,omitempty"`

	// Entries contains all of the Request and Response details that passed
	// through this Client.
	Entries []Entry `json:"entries"`

	// Comment can be added to the log to describe the particulars of this data.
	Comment string `json:"comment,omitempty"`
}

// Page represents a group of requests (e.g. an HTML document with multiple resources)
type Page struct {
	// Start of the page load (ISO 8601)
	Start string `json:"startedDateTime"`

	// ID used to reference this page grouping (Entry.PageRef)
	ID string `json:"id"`

	// Title of the page
	Title string `json:"title"`

	// PageTimings contains detailing timing info about the page load
	PageTimings struct {
		// OnContentLoad is milliseconds since Start for page content to be loaded.
		OnContentLoad int `json:"onContentLoad,omitempty"`

		// OnLoad is milliseconds since Start for OnLoad event to be fired.
		OnLoad int `json:"onLoad,omitempty"`

		// Comment can be added by the user
		Comment string `json:"comment,omitempty"`
	} `json:"pageTimings"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Entry describes one request-response pair and associated information.
type Entry struct {
	// PageRef references the parent page (if supported), Page.ID.
	PageRef string `json:"pageref,omitempty"`

	// Start of the request (ISO 8601)
	Start string `json:"startedDateTime"`

	// Total time in milliseconds, Time=SUM(Timings.*)
	Time int `json:"time"`

	// Request details
	Request Request `json:"request"`

	// Response details
	Response Response `json:"response"`

	// Cache contains info about how the request was/is now cached.
	Cache struct {
		Before *CacheInfo `json:"beforeRequest,omitempty"`
		After  *CacheInfo `json:"afterRequest,omitempty"`

		// Comment can be added by the user
		Comment string `json:"comment,omitempty"`
	} `json:"cache"`

	// Timings contains detail info about the request/response round trip.
	Timings struct {
		// Send is the Time required to send this request to the server.
		Send int `json:"send"`
		// Wait is the Time spent waiting on a response from the server.
		Wait int `json:"wait"`
		// Receive is the Time spent reading the entire response from the server.
		Receive int `json:"receive"`

		// Blocked is the Time spent in a queue waiting for a network connection
		Blocked int `json:"blocked,omitempty"`
		// DNS is the domain name resolution time - The time required to resolve a host name
		DNS int `json:"dns,omitempty"`
		// Connect is the Time required to create TCP connection.
		Connect int `json:"connect,omitempty"`

		// SSL is the Time required to negotiate the SSL/TLS connection.
		// Note: if defined this time is included in Connect.
		SSL int `json:"ssl,omitempty"`

		// Comment can be added by the user
		Comment string `json:"comment,omitempty"`
	} `json:"timings"`

	// ServerIP contains the connected server address.
	ServerIP string `json:"serverIPAddress,omitempty"`

	// Connection contains the connection info (e.g. a TCP/IP Port/ID)
	Connection string `json:"connection,omitempty"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Request contains the request description and content.
type Request struct {
	// Method of the HTTP request, in caps, GET/POST/etc
	Method string `json:"method"`

	// URL of the request (absolute), with fragments removed.
	URL string `json:"url"`

	// HTTPVersion of the request
	HTTPVersion string `json:"httpVersion"` // ex "HTTP/1.1"

	// Cookies sent with the request
	Cookies []Cookie `json:"cookies"`

	// Headers sent with the request
	Headers []NameValuePair `json:"headers"`

	// QueryParams parsed from the URL
	QueryParams []NameValuePair `json:"queryString"`

	// Body of the request (e.g. from a POST)
	Body struct {
		// MIMEType of the body content
		MIMEType string `json:"mimeType"`
		// List of (parsed URL-encoded) parameters, exclusive with Content
		Params []PostNameValuePair `json:"params,omitempty"`
		// Content of the post as plain text (exclusive with Params)
		Content string `json:"text,omitempty"`
	} `json:"postData,omitempty"`

	// HeadersSize of the request header in bytes.
	// NB counted from start of request to end of double CRLF before body.
	HeadersSize int `json:"headersSize"`

	// BodySize of the request body in bytes.
	BodySize int `json:"bodySize"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// PostNameValuePair contains the description and content of a POSTed name and value pair.
// In particular this can include files.
type PostNameValuePair struct {
	// Name of the posted parameter
	Name string `json:"name"`
	// Value of the parameter or file contents
	Value string `json:"value,omitempty"`
	// Name of an uploaded file
	FileName string `json:"fileName,omitempty"`
	// ContentType of an uploaded file
	ContentType string `json:"contentType,omitempty"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Response contains the response description and content.
type Response struct {
	// StatusCode indicates the response status
	StatusCode int `json:"status"` // 200

	// StatusText describes the response status
	StatusText string `json:"statusText"` // "OK"

	// HTTPVersion of the HTTP response
	HTTPVersion string `json:"httpVersion"` // ex "HTTP/1.1"

	// RedirectURL from the location header
	RedirectURL string `json:"redirectURL"`

	// Cookies sent with the response
	Cookies []Cookie `json:"cookies"`

	// Headers sent with the response
	// NB Headers may include values added by the browser but not included in server's response.
	Headers []NameValuePair `json:"headers"`

	// Body describes the response body content.
	Body struct {
		// Size of response content in bytes.
		Size int `json:"size"`
		// Compression is the number of bytes saved by compression
		Compression int `json:"compression,omitempty"`
		// MIMEType of the body content
		MIMEType string `json:"mimeType"`
		// Content is the text content of the response body.
		Content string `json:"text,omitempty"`
		// Encoding used by the response.
		Encoding string `json:"encoding,omitempty"`
		// Comment can be added by the user
		Comment string `json:"comment,omitempty"`
	} `json:"content"`

	// HeadersSize of the request header in bytes.
	// NB counted from start of request to end of double CRLF before body.
	// NB only includes the size of headers sent by the server, not those added by a browser.
	HeadersSize int `json:"headersSize"`

	// BodySize of the response body in bytes
	BodySize int `json:"bodySize"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// NameValuePair is a name and value, paired.
type NameValuePair struct {
	// Name of the parameter
	Name string `json:"name"`
	// Value of the parameter
	Value string `json:"value"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// CacheInfo describes cache properties of known content
type CacheInfo struct {
	// Expiration time of the cached content (ISO 8601)
	Expires string `json:"expires,optional"`

	// LastAccess time of the cached content (ISO 8601)
	LastAccess string `json:"lastAccess"`

	// ETag referencing the cached content
	ETag string `json:"etag"`

	// HitCount is the number of the times the cached content has been opened.
	HitCount int `json:"hitCount"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Cookie describes the cookie information for requests and responses.
type Cookie struct {
	// Name of the cookie.
	Name string `json:"name"`
	// Value stored in the cookie.
	Value string `json:"value"`
	// Path that this cookie applied to.
	Path string `json:"path,omitempty"`
	// Domain is the hostname the cookie applies to.
	Domain string `json:"domain,omitempty"`
	// Expires describes the cookie expiration time (ISO 8601).
	Expires string `json:"expires,omitempty"`
	// Secure is true if the cookie was transferred over SSL.
	Secure bool `json:"secure,omitempty"`
	// HTTPOnly flag status of the cookie.
	HTTPOnly bool `json:"httpOnly,omitempty"`
}

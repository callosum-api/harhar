package harhar

import "time"

// FormatVersion is the version of the HAR format these types implement.
const FormatVersion = "1.2"

// HAR represents the root of an HTTP Archive document. Field and type names
// follow the HAR 1.2 spec, and the types can be used to read HAR files as
// well as write them.
//
// Spec: http://www.softwareishard.com/blog/har-12-spec/
type HAR struct {
	Log Log `json:"log"`
}

// NewHAR creates an empty HTTP Archive document with the given creator.
func NewHAR(creatorName, creatorVersion string) *HAR {
	return &HAR{
		Log: Log{
			Version: FormatVersion,
			Creator: Creator{Name: creatorName, Version: creatorVersion},
			Entries: []Entry{},
		},
	}
}

// Creator describes the application that created the log, or the browser
// that made the requests.
type Creator struct {
	// Name of the application.
	Name string `json:"name"`

	// Version of the application.
	Version string `json:"version"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Log represents a set of HTTP Request/Response Entries.
type Log struct {
	// Version of the HAR format, FormatVersion.
	Version string `json:"version"`

	// Creator of this set of Log entries.
	Creator Creator `json:"creator"`

	// Browser information that produced this set of Log entries.
	Browser *Creator `json:"browser,omitempty"`

	// Pages contain information about request groupings, such as a page loaded by a web browser.
	Pages []Page `json:"pages,omitempty"`

	// Entries contains all of the recorded requests and responses.
	Entries []Entry `json:"entries"`

	// Comment can be added to the log to describe the particulars of this data.
	Comment string `json:"comment,omitempty"`
}

// Page represents a group of requests (e.g. an HTML document with multiple resources)
type Page struct {
	// StartedDateTime of the page load.
	StartedDateTime time.Time `json:"startedDateTime"`

	// ID used to reference this page grouping (Entry.PageRef)
	ID string `json:"id"`

	// Title of the page
	Title string `json:"title"`

	// PageTimings contains detailing timing info about the page load
	PageTimings PageTimings `json:"pageTimings"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// PageTimings contains DOM-related page timing information.
type PageTimings struct {
	// OnContentLoad is milliseconds since StartedDateTime for page content
	// to be loaded, or -1 if not applicable.
	OnContentLoad float64 `json:"onContentLoad,omitempty"`

	// OnLoad is milliseconds since StartedDateTime for the load event to
	// fire, or -1 if not applicable.
	OnLoad float64 `json:"onLoad,omitempty"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Entry describes one request-response pair and associated information.
type Entry struct {
	// PageRef references the parent page (if supported), Page.ID.
	PageRef string `json:"pageref,omitempty"`

	// StartedDateTime of the request.
	StartedDateTime time.Time `json:"startedDateTime"`

	// Time is the total elapsed time of the request in milliseconds.
	Time float64 `json:"time"`

	// Request details
	Request Request `json:"request"`

	// Response details
	Response Response `json:"response"`

	// Cache contains info about how the request was/is now cached.
	Cache Cache `json:"cache"`

	// Timings contains detail info about the request/response round trip.
	Timings Timings `json:"timings"`

	// ServerIP contains the connected server address.
	ServerIP string `json:"serverIPAddress,omitempty"`

	// Connection contains the connection info (e.g. a TCP/IP Port/ID)
	Connection string `json:"connection,omitempty"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Cache represents the cache status before and after a request.
type Cache struct {
	// Before contains the cache status before the request
	Before *CacheInfo `json:"beforeRequest,omitempty"`

	// After contains the cache status after the request
	After *CacheInfo `json:"afterRequest,omitempty"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Timings breaks down the time spent on a request, in milliseconds.
// Blocked, DNS, Connect and SSL are -1 when they don't apply, such as on a
// reused connection.
type Timings struct {
	// Blocked is the time spent waiting for a network connection.
	Blocked float64 `json:"blocked"`
	// DNS is the time spent resolving the host name.
	DNS float64 `json:"dns"`
	// Connect is the time spent creating the connection, including SSL.
	Connect float64 `json:"connect"`
	// Send is the time spent sending the request.
	Send float64 `json:"send"`
	// Wait is the time spent waiting for the first byte of the response.
	Wait float64 `json:"wait"`
	// Receive is the time spent reading the response body.
	Receive float64 `json:"receive"`
	// SSL is the time spent on the TLS handshake. It is included in Connect.
	SSL float64 `json:"ssl"`

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

	// QueryString parameters parsed from the URL
	QueryString []NameValuePair `json:"queryString"`

	// PostData of the request (e.g. from a POST), omitted when there is none.
	PostData PostData `json:"postData,omitzero"`

	// HeadersSize of the request header in bytes, or -1 if unknown.
	HeadersSize int64 `json:"headersSize"`

	// BodySize of the request body in bytes, or -1 if unknown.
	BodySize int64 `json:"bodySize"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// PostData describes the body of a request.
type PostData struct {
	// MIMEType of the body content
	MIMEType string `json:"mimeType"`
	// Params parsed from a form body. Exclusive with Text.
	Params []PostParam `json:"params,omitempty"`
	// Text of the body. Exclusive with Params.
	Text string `json:"text,omitempty"`
	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// PostParam is a posted form field or uploaded file.
type PostParam struct {
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
	// Status code of the response, or 0 if the request failed.
	Status int `json:"status"`

	// StatusText describes the response status
	StatusText string `json:"statusText"` // "OK"

	// HTTPVersion of the HTTP response
	HTTPVersion string `json:"httpVersion"` // ex "HTTP/1.1"

	// RedirectURL from the location header
	RedirectURL string `json:"redirectURL"`

	// Cookies sent with the response
	Cookies []Cookie `json:"cookies"`

	// Headers sent with the response
	Headers []NameValuePair `json:"headers"`

	// Content describes the response body.
	Content Content `json:"content"`

	// HeadersSize of the response header in bytes, or -1 if unknown.
	HeadersSize int64 `json:"headersSize"`

	// BodySize of the response body in bytes as sent, or -1 if unknown.
	BodySize int64 `json:"bodySize"`

	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

// Content describes the body of a response.
type Content struct {
	// Size of the body in bytes, after decompression.
	Size int64 `json:"size"`
	// Compression is the number of bytes saved by compression
	Compression int64 `json:"compression,omitempty"`
	// MIMEType of the body content
	MIMEType string `json:"mimeType"`
	// Text of the body, encoded as described by Encoding.
	Text string `json:"text,omitempty"`
	// Encoding of Text: "" for plain text, or "base64".
	Encoding string `json:"encoding,omitempty"`
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
	// Expires is when the cached content expires.
	Expires time.Time `json:"expires,omitzero"`

	// LastAccess is when the cached content was last used.
	LastAccess time.Time `json:"lastAccess"`

	// ETag referencing the cached content
	ETag string `json:"eTag"`

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
	// Expires is when the cookie expires, if set.
	Expires time.Time `json:"expires,omitzero"`
	// Secure is true if the cookie was transferred over SSL.
	Secure bool `json:"secure,omitempty"`
	// HTTPOnly flag status of the cookie.
	HTTPOnly bool `json:"httpOnly,omitempty"`
	// Comment can be added by the user
	Comment string `json:"comment,omitempty"`
}

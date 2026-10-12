package harhar

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// bodyCapture keeps up to limit bytes of a body as it streams past, and
// counts the total. It is safe for concurrent use, since a transport may
// still be sending a request body when its response completes.
type bodyCapture struct {
	mu    sync.Mutex
	limit int64 // negative for no limit
	buf   []byte
	size  int64
}

func newBodyCapture(limit int64) *bodyCapture {
	return &bodyCapture{limit: limit}
}

// Write records p, keeping only what fits under the limit. It never fails.
func (c *bodyCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.size += int64(len(p))
	keep := p
	if c.limit >= 0 {
		room := max(c.limit-int64(len(c.buf)), 0)
		keep = p[:min(int64(len(p)), room)]
	}
	c.buf = append(c.buf, keep...)
	return len(p), nil
}

func (c *bodyCapture) reset() {
	c.mu.Lock()
	c.buf, c.size = c.buf[:0], 0
	c.mu.Unlock()
}

// snapshot returns a copy of the recorded bytes and the total body size.
func (c *bodyCapture) snapshot() ([]byte, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf), c.size
}

// errClosedEarly is reported when a body is closed before reaching EOF.
var errClosedEarly = errors.New("body closed before it was fully read")

// wrap returns a body that reads from rc while recording into c. If onDone
// is not nil, it is called once: with nil at EOF, with the error if a read
// fails, or with errClosedEarly if the body is closed first.
func (c *bodyCapture) wrap(rc io.ReadCloser, onDone func(error)) io.ReadCloser {
	return &teeBody{rc: rc, c: c, onDone: onDone}
}

type teeBody struct {
	rc     io.ReadCloser
	c      *bodyCapture
	onDone func(error)
	once   sync.Once
}

func (b *teeBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.c.Write(p[:n])
	if err == io.EOF {
		b.done(nil)
	} else if err != nil {
		b.done(err)
	}
	return n, err
}

func (b *teeBody) Close() error {
	err := b.rc.Close()
	b.done(errClosedEarly)
	return err
}

func (b *teeBody) done(err error) {
	if b.onDone != nil {
		b.once.Do(func() { b.onDone(err) })
	}
}

// makeRequest converts hr to a harhar.Request, without its body.
func (cfg *config) makeRequest(hr *http.Request) Request {
	r := Request{
		Method:      hr.Method,
		URL:         requestURL(hr),
		HTTPVersion: hr.Proto,
		Cookies:     makeCookies(hr.Cookies(), cfg.redact["Cookie"]),
		Headers:     cfg.makeHeaders(hr.Header),
		QueryString: parseQuery(hr.URL.RawQuery),
		HeadersSize: headersSize(hr.Header),
	}
	if r.QueryString == nil {
		r.QueryString = []NameValuePair{}
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

// setPostData records a captured request body in r, parsing form
// parameters when the content type is a form encoding.
func (cfg *config) setPostData(r *Request, contentType string, body *bodyCapture) {
	data, size := body.snapshot()
	r.BodySize = size
	if size == 0 {
		return
	}

	pd := PostData{MIMEType: contentType}
	if pd.MIMEType == "" {
		// default per RFC 9110 section 8.3
		pd.MIMEType = "application/octet-stream"
	}
	defer func() { r.PostData = pd }()

	if int64(len(data)) < size {
		// A truncated form can't be parsed, so keep it as text.
		pd.Text = string(trimIncompleteRune(data))
		pd.Comment = truncatedComment(len(data), size)
		return
	}

	if mediaType, params, err := mime.ParseMediaType(pd.MIMEType); err == nil {
		switch mediaType {
		case "application/x-www-form-urlencoded":
			for _, p := range parseQuery(string(data)) {
				pd.Params = append(pd.Params, PostParam{Name: p.Name, Value: p.Value})
			}
		case "multipart/form-data":
			// On a malformed body, fall back to recording the raw text.
			pd.Params, _ = multipartParams(data, params["boundary"])
		}
	}

	// HAR's params and text are mutually exclusive.
	if pd.Params == nil {
		pd.Text = string(data)
	}
}

// multipartParams parses a multipart/form-data body into HAR params, in the
// order they were sent.
func multipartParams(body []byte, boundary string) ([]PostParam, error) {
	if boundary == "" {
		return nil, errors.New("multipart body has no boundary")
	}
	var params []PostParam
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
		params = append(params, PostParam{
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

// makeHeaders converts headers to HAR name/value pairs, sorted by name so
// the output is deterministic, with configured headers redacted.
func (cfg *config) makeHeaders(h http.Header) []NameValuePair {
	pairs := make([]NameValuePair, 0, len(h))
	for _, name := range slices.Sorted(maps.Keys(h)) {
		redact := cfg.redact[http.CanonicalHeaderKey(name)]
		for _, val := range h[name] {
			if redact {
				val = RedactedValue
			}
			pairs = append(pairs, NameValuePair{Name: name, Value: val})
		}
	}
	return pairs
}

func makeCookies(cookies []*http.Cookie, redact bool) []Cookie {
	out := make([]Cookie, 0, len(cookies))
	for _, c := range cookies {
		nc := Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Path:     c.Path,
			Domain:   c.Domain,
			Expires:  c.Expires,
			HTTPOnly: c.HttpOnly,
			Secure:   c.Secure,
		}
		if redact {
			nc.Value = RedactedValue
		}
		out = append(out, nc)
	}
	return out
}

// headersSize approximates the size of the header block in bytes.
func headersSize(h http.Header) int64 {
	var buf bytes.Buffer
	h.Write(&buf)
	return int64(buf.Len()) + 4 // incl. CRLF CRLF
}

// makeResponse converts hr to a harhar.Response, without its body.
func (cfg *config) makeResponse(hr *http.Response) Response {
	r := Response{
		Status:      hr.StatusCode,
		StatusText:  statusText(hr),
		HTTPVersion: hr.Proto,
		Cookies:     makeCookies(hr.Cookies(), cfg.redact["Set-Cookie"]),
		Headers:     cfg.makeHeaders(hr.Header),
		HeadersSize: headersSize(hr.Header),
	}
	if rurl, err := hr.Location(); err == nil {
		r.RedirectURL = rurl.String()
	}
	return r
}

// makeContent converts a captured response body to HAR content.
func (cfg *config) makeContent(h http.Header, body *bodyCapture) Content {
	data, size := body.snapshot()
	c := Content{Size: size, MIMEType: h.Get("Content-Type")}
	if c.MIMEType == "" {
		if len(data) > 0 {
			// As net/http would when serving the response.
			c.MIMEType = http.DetectContentType(data)
		} else {
			// default per RFC 9110 section 8.3
			c.MIMEType = "application/octet-stream"
		}
	}
	if int64(len(data)) < size {
		data = trimIncompleteRune(data)
		c.Comment = truncatedComment(len(data), size)
	}

	// Content in other charsets is not transcoded to UTF-8 as the spec asks,
	// but anything that isn't valid UTF-8 is base64-encoded so that no data
	// is lost in the JSON output.
	if utf8.Valid(data) {
		c.Text = string(data)
	} else {
		c.Text = base64.StdEncoding.EncodeToString(data)
		c.Encoding = "base64"
	}
	return c
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
		Content:     Content{MIMEType: "x-unknown"},
		HeadersSize: -1,
		BodySize:    -1,
		Comment:     err.Error(),
	}
}

func truncatedComment(kept int, size int64) string {
	return fmt.Sprintf("body truncated: recorded %d of %d bytes", kept, size)
}

// trimIncompleteRune drops a partial UTF-8 sequence from the end of b, as
// left by truncating a body, so valid text stays valid.
func trimIncompleteRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

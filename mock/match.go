package mock

import (
	"bytes"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/callosum-api/harhar"
)

// DefaultMatcher is the matcher used unless WithMatcher is given. It
// matches a request to an entry with the same method, path and query
// parameters, in any order. It ignores the host, headers and body; the
// Mock itself prefers entries for the same host and with the same body.
//
// To relax it, adjust copies of the request and entry and fall back to it:
//
//	mock.WithMatcher(func(req *http.Request, e harhar.Entry) bool {
//		// Ignore a cache-busting "_" parameter in both URLs.
//		r := req.Clone(req.Context())
//		q := r.URL.Query()
//		q.Del("_")
//		r.URL.RawQuery = q.Encode()
//		u, err := url.Parse(e.Request.URL)
//		if err != nil {
//			return false
//		}
//		q = u.Query()
//		q.Del("_")
//		u.RawQuery = q.Encode()
//		e.Request.URL = u.String() // e is a copy
//		return mock.DefaultMatcher(r, e)
//	})
func DefaultMatcher(req *http.Request, e harhar.Entry) bool {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	if method != e.Request.Method {
		return false
	}
	u, err := url.Parse(e.Request.URL)
	if err != nil {
		return false
	}
	return cleanPath(req.URL.Path) == cleanPath(u.Path) && sameQuery(req.URL.RawQuery, u.RawQuery)
}

func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// sameQuery reports whether two query strings have the same parameters.
// Repeated parameters must be in the same order.
func sameQuery(a, b string) bool {
	if a == b {
		return true
	}
	qa, _ := url.ParseQuery(a)
	qb, _ := url.ParseQuery(b)
	return maps.EqualFunc(qa, qb, slices.Equal)
}

// bodyMatches reports whether body is the body recorded in r.
func bodyMatches(r *harhar.Request, contentType string, body []byte) bool {
	pd := r.PostData
	switch {
	case len(pd.Params) > 0:
		got := formParams(contentType, body)
		return slices.EqualFunc(got, pd.Params, func(a, b harhar.PostParam) bool {
			return a.Name == b.Name && a.Value == b.Value && a.FileName == b.FileName
		})
	case pd.Text != "":
		if r.BodySize > int64(len(pd.Text)) {
			// Only the start of a long body was recorded.
			return bytes.HasPrefix(body, []byte(pd.Text))
		}
		return string(body) == pd.Text
	default:
		return len(body) == 0
	}
}

// formParams parses a form body as harhar records it, or returns nil if it
// isn't one.
func formParams(contentType string, body []byte) []harhar.PostParam {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil
	}
	var out []harhar.PostParam
	switch mediaType {
	case "application/x-www-form-urlencoded":
		for pair := range strings.SplitSeq(string(body), "&") {
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
			out = append(out, harhar.PostParam{Name: name, Value: value})
		}
	case "multipart/form-data":
		if params["boundary"] == "" {
			return nil
		}
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil
			}
			value, err := io.ReadAll(part)
			if err != nil {
				return nil
			}
			out = append(out, harhar.PostParam{
				Name:     part.FormName(),
				Value:    string(value),
				FileName: part.FileName(),
			})
		}
	}
	return out
}

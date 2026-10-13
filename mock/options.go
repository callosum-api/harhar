package mock

import (
	"net/http"
	"strings"

	"github.com/callosum-api/harhar"
)

// An Option configures a Mock.
type Option func(*config)

type config struct {
	hosts   map[string]string // normalized recorded host -> replacement host
	matcher func(req *http.Request, e harhar.Entry) bool
}

func defaultConfig() config {
	return config{hosts: make(map[string]string), matcher: DefaultMatcher}
}

// WithHost answers requests for host with the entries recorded from
// recorded, and rewrites URLs naming recorded to name host instead. Both
// are hosts as they appear in a URL, with a port if it isn't the default,
// such as "api.example.com" or "127.0.0.1:8080".
//
// Use it on the client side to point recorded traffic at a different host,
// such as a staging server. On the server side, every recorded host is
// already rewritten to the mock server's own host; WithHost overrides that
// for recorded.
func WithHost(recorded, host string) Option {
	return func(c *config) { c.hosts[strings.ToLower(recorded)] = strings.ToLower(host) }
}

// WithMatcher sets the function that decides whether a request matches a
// recorded entry, replacing DefaultMatcher. It is called only for entries
// recorded from the request's host (on the client side), and may read
// req.Body, which is a fresh copy for each call. A nil fn restores
// DefaultMatcher.
//
// fn may be called concurrently. e is a copy, but its slices are shared
// with the Mock, so don't modify their elements.
func WithMatcher(fn func(req *http.Request, e harhar.Entry) bool) Option {
	return func(c *config) {
		if fn == nil {
			fn = DefaultMatcher
		}
		c.matcher = fn
	}
}

package harhar

import (
	"net/http"
	"runtime/debug"
)

// DefaultMaxBodySize is how many bytes of each request and response body
// are recorded unless WithMaxBodySize is used.
const DefaultMaxBodySize = 1 << 20

// RedactedValue replaces the values of redacted headers and cookies.
const RedactedValue = "[REDACTED]"

// DefaultRedactedHeaders returns the headers that are redacted unless
// WithRedactedHeaders is used.
func DefaultRedactedHeaders() []string {
	return []string{"Authorization", "Cookie", "Proxy-Authorization", "Set-Cookie"}
}

const modulePath = "github.com/callosum-api/harhar"

// An Option configures a Recorder.
type Option func(*config)

type config struct {
	creator     Creator
	redact      map[string]bool // canonical header names
	maxBodySize int64           // negative for no limit
	maxEntries  int             // negative for no limit
	onEntry     func(Entry)
}

func defaultConfig() config {
	cfg := config{
		creator:     Creator{Name: "harhar", Version: moduleVersion()},
		maxBodySize: DefaultMaxBodySize,
		maxEntries:  -1,
	}
	WithRedactedHeaders(DefaultRedactedHeaders()...)(&cfg)
	return cfg
}

// moduleVersion returns this module's version from the build info.
func moduleVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Path == modulePath && bi.Main.Version != "" {
			return bi.Main.Version
		}
		for _, dep := range bi.Deps {
			if dep.Path == modulePath {
				return dep.Version
			}
		}
	}
	return "(devel)"
}

// WithCreator sets the application recorded as the log's creator. The
// default is "harhar" at this module's version.
func WithCreator(name, version string) Option {
	return func(c *config) { c.creator = Creator{Name: name, Version: version} }
}

// WithRedactedHeaders sets which headers have their values replaced with
// RedactedValue, replacing DefaultRedactedHeaders. Redacting Cookie or
// Set-Cookie also redacts the values of the cookies parsed from them. Call
// it with no names to record every header as is.
func WithRedactedHeaders(names ...string) Option {
	return func(c *config) {
		c.redact = make(map[string]bool, len(names))
		for _, name := range names {
			c.redact[http.CanonicalHeaderKey(name)] = true
		}
	}
}

// WithMaxBodySize limits how many bytes of each request and response body
// are recorded. Bodies are always sent and received in full; only the
// recorded copy is truncated. Zero records no body content, and a negative
// size records bodies in full. The default is DefaultMaxBodySize.
func WithMaxBodySize(n int64) Option {
	return func(c *config) { c.maxBodySize = n }
}

// WithMaxEntries keeps only the n most recent entries. Zero keeps none,
// which is useful together with WithOnEntry. By default every entry is kept.
func WithMaxEntries(n int) Option {
	return func(c *config) { c.maxEntries = n }
}

// WithOnEntry calls fn with each entry as it is recorded, for example to
// stream entries somewhere instead of keeping them in memory. fn may be
// called concurrently, and must not modify the entry.
func WithOnEntry(fn func(Entry)) Option {
	return func(c *config) { c.onEntry = fn }
}

package harhar

import "runtime/debug"

// DefaultMaxBodySize is how many bytes of each request and response body
// are recorded unless WithMaxBodySize is used.
const DefaultMaxBodySize = 32 << 20

const modulePath = "github.com/callosum-api/harhar"

// An Option configures a Recorder.
type Option func(*config)

type config struct {
	creator      Creator
	redactHeader func(name, value string) string // nil records headers as is
	maxBodySize  int64                           // negative for no limit
	maxEntries   int                             // negative for no limit
	onEntry      func(Entry)
}

func defaultConfig() config {
	return config{
		creator:      Creator{Name: "harhar", Version: moduleVersion()},
		redactHeader: DefaultHeaderRedactor,
		maxBodySize:  DefaultMaxBodySize,
		maxEntries:   -1,
	}
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

// WithHeaderRedactor sets the function that redacts header values before
// they are recorded, replacing DefaultHeaderRedactor. It is called with the
// canonical header name (e.g. "Set-Cookie") for each value of every
// request and response header, and returns the value to record.
//
// The recorded cookie lists are parsed from the redacted Cookie and
// Set-Cookie headers, so they match whatever the redactor recorded; a
// cookie the redactor makes unparsable is left out of the list.
//
// A nil fn records every header as is. fn may be called concurrently.
func WithHeaderRedactor(fn func(name, value string) string) Option {
	return func(c *config) { c.redactHeader = fn }
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

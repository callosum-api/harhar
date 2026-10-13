// Package hostrewrite rewrites the hosts in URLs found in text, such as
// HTTP headers and bodies, so recorded traffic can point at a different
// server.
package hostrewrite

import (
	"cmp"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Target is where URLs naming a host point instead.
type Target struct {
	Scheme string // "" keeps each URL's own scheme
	Host   string
}

// Rewriter rewrites URLs naming a fixed set of hosts. A nil Rewriter
// rewrites nothing.
type Rewriter struct {
	re *regexp.Regexp
}

// New returns a Rewriter for URLs naming any of hosts, which should be
// normalized with NormalizeHost. It returns nil if there are no hosts.
func New(hosts []string) *Rewriter {
	if len(hosts) == 0 {
		return nil
	}
	// Longest first, since the first alternative that matches wins.
	hosts = slices.Clone(hosts)
	slices.SortFunc(hosts, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	alts := make([]string, len(hosts))
	for i, h := range hosts {
		alts[i] = regexp.QuoteMeta(h)
	}
	// An http, https or protocol-relative URL up to the end of its host.
	// Slashes may be escaped, as some JSON encoders do. Group 1 is the
	// scheme with its colon, group 2 the slashes, and group 3 the host.
	return &Rewriter{re: regexp.MustCompile(`(?i)(https?:)?(\\?/\\?/)(` + strings.Join(alts, "|") + `)`)}
}

// Rewrite returns s with each URL that names a host in to pointing at that
// host's target instead. Bare hostnames, outside a URL, are left alone.
func (r *Rewriter) Rewrite(s string, to map[string]Target) string {
	if r == nil || len(to) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range r.re.FindAllStringSubmatchIndex(s, -1) {
		end := loc[1]
		if !hostEnds(s[end:]) {
			continue // e.g. api.example.com.evil.net, or another port
		}
		t, ok := to[strings.ToLower(s[loc[6]:loc[7]])]
		if !ok {
			continue
		}
		b.WriteString(s[last:loc[0]])
		if loc[2] >= 0 {
			scheme := t.Scheme
			if scheme == "" {
				scheme = s[loc[2] : loc[3]-1]
			}
			b.WriteString(scheme + ":")
		}
		b.WriteString(s[loc[4]:loc[5]])
		b.WriteString(t.Host)
		last = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// hostEnds reports whether a host followed by rest ends there, rather than
// continuing with more of a hostname or a port.
func hostEnds(rest string) bool {
	if rest == "" {
		return true
	}
	switch c := rest[0]; {
	case isHostChar(c):
		return false
	case c == '.' || c == ':':
		// A trailing dot ends a sentence; a dot or colon followed by more
		// is another host or a port.
		return len(rest) == 1 || !isHostChar(rest[1])
	}
	return true
}

func isHostChar(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_'
}

// DropCookieDomain removes the Domain attribute from a Set-Cookie value if
// it covers a host that is being rewritten to a different name, so the
// cookie applies to the new host.
func DropCookieDomain(value string, to map[string]Target) string {
	parts := strings.Split(value, ";")
	kept := parts[:1]
	for _, attr := range parts[1:] {
		name, domain, _ := strings.Cut(strings.TrimSpace(attr), "=")
		if strings.EqualFold(name, "Domain") && coversRewritten(domain, to) {
			continue
		}
		kept = append(kept, attr)
	}
	return strings.Join(kept, ";")
}

func coversRewritten(domain string, to map[string]Target) bool {
	domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
	for old, t := range to {
		name := hostname(old)
		if name == hostname(t.Host) {
			continue
		}
		if name == domain || strings.HasSuffix(name, "."+domain) {
			return true
		}
	}
	return false
}

// hostname returns host without its port.
func hostname(host string) string {
	return (&url.URL{Host: host}).Hostname()
}

// NormalizeHost lowercases host and drops the default port for scheme.
func NormalizeHost(scheme, host string) string {
	host = strings.ToLower(host)
	switch strings.ToLower(scheme) {
	case "http":
		host = strings.TrimSuffix(host, ":80")
	case "https":
		host = strings.TrimSuffix(host, ":443")
	}
	return host
}

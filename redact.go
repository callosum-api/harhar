package harhar

import (
	"net/http"
	"strings"
)

// RedactedValue replaces the sensitive parts of redacted header values.
const RedactedValue = "[REDACTED]"

// DefaultHeaderRedactor is the header redactor used unless
// WithHeaderRedactor is given. It replaces only the secret part of each
// value with RedactedValue, keeping the rest for context:
//
//	Authorization: Bearer abc123            -> Bearer [REDACTED]
//	Proxy-Authorization: Basic dXNlcjpwYXNz -> Basic [REDACTED]
//	Cookie: id=abc; theme=dark              -> id=[REDACTED]; theme=[REDACTED]
//	Set-Cookie: id=abc; Path=/; HttpOnly    -> id=[REDACTED]; Path=/; HttpOnly
//
// Other headers are returned unchanged. To redact more, wrap it:
//
//	harhar.WithHeaderRedactor(func(name, value string) string {
//		if name == "X-Api-Key" {
//			return harhar.RedactedValue
//		}
//		return harhar.DefaultHeaderRedactor(name, value)
//	})
func DefaultHeaderRedactor(name, value string) string {
	if value == "" {
		return value
	}
	switch name {
	case "Authorization", "Proxy-Authorization":
		return redactCredentials(value)
	case "Cookie":
		return redactCookies(value)
	case "Set-Cookie":
		return redactSetCookie(value)
	}
	return value
}

// redactCredentials redacts an "<auth-scheme> <credentials>" value,
// keeping the scheme.
func redactCredentials(value string) string {
	scheme, creds, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || strings.TrimSpace(creds) == "" {
		return RedactedValue
	}
	return scheme + " " + RedactedValue
}

// redactCookies redacts the values in a Cookie header, keeping the names.
func redactCookies(value string) string {
	parts := strings.Split(value, ";")
	for i, part := range parts {
		parts[i] = redactCookiePair(strings.TrimSpace(part))
	}
	return strings.Join(parts, "; ")
}

// redactSetCookie redacts the cookie value in a Set-Cookie header, keeping
// its name and attributes.
func redactSetCookie(value string) string {
	pair, attrs, ok := strings.Cut(value, ";")
	pair = redactCookiePair(strings.TrimSpace(pair))
	if !ok {
		return pair
	}
	return pair + ";" + attrs
}

// redactCookiePair redacts a "name=value" pair, or the whole of anything else.
func redactCookiePair(pair string) string {
	if pair == "" {
		return pair
	}
	if name, _, ok := strings.Cut(pair, "="); ok && name != "" {
		return name + "=" + RedactedValue
	}
	return RedactedValue
}

// redactHeaders returns a copy of h with every value passed through the
// configured redactor.
func (cfg *config) redactHeaders(h http.Header) http.Header {
	if cfg.redactHeader == nil {
		return h
	}
	out := make(http.Header, len(h))
	for name, vals := range h {
		canonical := http.CanonicalHeaderKey(name)
		redacted := make([]string, len(vals))
		for i, v := range vals {
			redacted[i] = cfg.redactHeader(canonical, v)
		}
		out[name] = redacted
	}
	return out
}

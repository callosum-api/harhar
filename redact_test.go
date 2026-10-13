package harhar

import "testing"

func TestDefaultHeaderRedactor(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"Authorization", "Bearer abc.def.ghi", "Bearer [REDACTED]"},
		{"Authorization", "Basic dXNlcjpwYXNz", "Basic [REDACTED]"},
		{"Authorization", `Digest username="u", response="abc"`, "Digest [REDACTED]"},
		{"Authorization", "rawtoken", "[REDACTED]"},
		{"Authorization", "", ""},
		{"Proxy-Authorization", "Basic dXNlcjpwYXNz", "Basic [REDACTED]"},
		{"Cookie", "id=abc", "id=[REDACTED]"},
		{"Cookie", "id=abc; theme=dark;lang=en", "id=[REDACTED]; theme=[REDACTED]; lang=[REDACTED]"},
		{"Cookie", "id=", "id=[REDACTED]"},
		{"Cookie", "valueonly", "[REDACTED]"},
		{"Set-Cookie", "id=abc", "id=[REDACTED]"},
		{"Set-Cookie", "id=abc; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT; HttpOnly",
			"id=[REDACTED]; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT; HttpOnly"},
		{"X-Other", "Bearer abc", "Bearer abc"},
	} {
		if got := DefaultHeaderRedactor(tc.name, tc.in); got != tc.want {
			t.Errorf("DefaultHeaderRedactor(%q, %q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

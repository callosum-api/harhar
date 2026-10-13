package headerfile

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/callosum-api/harhar"
)

func TestParseHeaders(t *testing.T) {
	// CRLF line endings, a trailing newline, blank lines, colons in values,
	// and a repeated name used to panic or be misparsed.
	in := "Authorization: Bearer abc\r\n\r\nx-trace:  t1 \nX-Trace: t2\nReferer: https://example.com/\n"
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := http.Header{
		"Authorization": {"Bearer abc"},
		"X-Trace":       {"t2"},
		"Referer":       {"https://example.com/"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseHeadersInvalid(t *testing.T) {
	for _, in := range []string{"no colon here\n", ": empty name\n"} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", in)
		}
	}
}

func TestHeaderRedactor(t *testing.T) {
	redact := Redactor(http.Header{"X-Token": {"secret"}, "Authorization": {"Bearer abc"}})
	for _, tc := range []struct{ name, in, want string }{
		{"X-Token", "secret", harhar.RedactedValue},
		{"Authorization", "Bearer abc", "Bearer " + harhar.RedactedValue}, // keeps the default's partial redaction
		{"Cookie", "id=1", "id=" + harhar.RedactedValue},
		{"X-Other", "visible", "visible"},
	} {
		if got := redact(tc.name, tc.in); got != tc.want {
			t.Errorf("redact(%q, %q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestParseHeaders(t *testing.T) {
	// CRLF line endings, a trailing newline, blank lines, colons in values,
	// and a repeated name used to panic or be misparsed.
	in := "Authorization: Bearer abc\r\n\r\nx-trace:  t1 \nX-Trace: t2\nReferer: https://example.com/\n"
	got, err := parseHeaders(strings.NewReader(in))
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
		if _, err := parseHeaders(strings.NewReader(in)); err == nil {
			t.Errorf("parseHeaders(%q) succeeded, want error", in)
		}
	}
}

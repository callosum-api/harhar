// Package headerfile reads the header files that the harprox and harreplay
// commands take with -h: one "Name: value" header per line.
package headerfile

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/callosum-api/harhar"
)

// Load reads the headers in filename. See Parse.
func Load(filename string) (http.Header, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	return h, nil
}

// Redactor returns a header redactor that applies
// harhar.DefaultHeaderRedactor, and also fully redacts the headers in
// injected, which are usually credentials.
func Redactor(injected http.Header) func(name, value string) string {
	return func(name, value string) string {
		if redacted := harhar.DefaultHeaderRedactor(name, value); redacted != value {
			return redacted
		}
		if _, ok := injected[name]; ok {
			return harhar.RedactedValue
		}
		return value
	}
}

// Parse reads "Name: value" lines, skipping blank lines. A repeated
// name keeps its last value.
func Parse(r io.Reader) (http.Header, error) {
	h := make(http.Header)
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("line %d: want \"Name: value\", got %q", n, line)
		}
		h.Set(name, strings.TrimSpace(value))
	}
	return h, sc.Err()
}

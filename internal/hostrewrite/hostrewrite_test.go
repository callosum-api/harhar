package hostrewrite

import "testing"

func TestRewrite(t *testing.T) {
	r := New([]string{"api.example.com", "localhost:8080", "localhost"})
	to := map[string]Target{
		"api.example.com": {Scheme: "http", Host: "127.0.0.1:5555"},
		"localhost:8080":  {Host: "mock.test"},
	}
	tests := []struct{ in, want string }{
		{"https://api.example.com/a", "http://127.0.0.1:5555/a"},
		{"HTTPS://API.Example.com/a", "http://127.0.0.1:5555/a"},
		{"http://api.example.com", "http://127.0.0.1:5555"},
		{`"https:\/\/api.example.com\/a"`, `"http:\/\/127.0.0.1:5555\/a"`},
		{"<a href=//api.example.com/a>", "<a href=//127.0.0.1:5555/a>"},
		{"see https://api.example.com.", "see http://127.0.0.1:5555."},
		{"https://api.example.com?q", "http://127.0.0.1:5555?q"},
		{"https://api.example.com#f", "http://127.0.0.1:5555#f"},
		{"https://user@api.example.com/", "https://user@api.example.com/"},
		{"https://api.example.com.evil.net/", "https://api.example.com.evil.net/"},
		{"https://api.example.community/", "https://api.example.community/"},
		{"https://api.example.com:8443/", "https://api.example.com:8443/"},
		{"api.example.com", "api.example.com"},
		{"https://xapi.example.com/", "https://xapi.example.com/"},
		{"http://localhost:8080/a", "http://mock.test/a"},
		{"https://localhost:8080/a", "https://mock.test/a"},
		{"http://localhost:80801/a", "http://localhost:80801/a"},
		{"http://localhost/a", "http://localhost/a"}, // no target
		{"a https://api.example.com b http://localhost:8080 c", "a http://127.0.0.1:5555 b http://mock.test c"},
	}
	for _, tt := range tests {
		if got := r.Rewrite(tt.in, to); got != tt.want {
			t.Errorf("Rewrite(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	var none *Rewriter
	if got := none.Rewrite("https://api.example.com", to); got != "https://api.example.com" {
		t.Errorf("nil Rewriter changed the text to %q", got)
	}
}

func TestDropCookieDomain(t *testing.T) {
	to := map[string]Target{
		"api.example.com": {Host: "127.0.0.1:5555"},
		"localhost:8080":  {Host: "localhost:9090"}, // same name, new port
	}
	tests := []struct{ in, want string }{
		{"a=1; Domain=api.example.com; Path=/", "a=1; Path=/"},
		{"a=1; domain=.example.com", "a=1"},
		{"a=1; Domain=example.org; Secure", "a=1; Domain=example.org; Secure"},
		{"a=1; Domain=localhost", "a=1; Domain=localhost"},
		{"a=1", "a=1"},
	}
	for _, tt := range tests {
		if got := DropCookieDomain(tt.in, to); got != tt.want {
			t.Errorf("DropCookieDomain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	tests := []struct{ scheme, host, want string }{
		{"https", "API.example.com:443", "api.example.com"},
		{"http", "example.com:80", "example.com"},
		{"http", "example.com:443", "example.com:443"},
		{"https", "[::1]:8443", "[::1]:8443"},
	}
	for _, tt := range tests {
		if got := NormalizeHost(tt.scheme, tt.host); got != tt.want {
			t.Errorf("NormalizeHost(%q, %q) = %q, want %q", tt.scheme, tt.host, got, tt.want)
		}
	}
}

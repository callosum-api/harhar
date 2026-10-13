package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/callosum-api/harhar"
	"github.com/callosum-api/harhar/mock"
)

// api is a small API. version 2 changes some of its responses.
func api(version int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/items", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Version", fmt.Sprint(version))
		name := "widget"
		if version == 2 {
			name = "gadget"
		}
		fmt.Fprintf(w, `{"items":[{"id":1,"name":%q,"updated":"%d"}],"next":"http://%s/v1/items?page=2"}`, name, version, r.Host)
	})
	mux.HandleFunc("POST /v1/items", func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "created %s", r.FormValue("name"))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer real" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "it's you")
	})
	mux.HandleFunc("GET /v1/text", func(w http.ResponseWriter, r *http.Request) {
		if version == 2 {
			io.WriteString(w, "line one\nline 2\n")
			return
		}
		io.WriteString(w, "line one\nline two\n")
	})
	return mux
}

// record makes requests to srv and returns them as a HAR file, with srv's
// host renamed to api.example.com.
func record(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	rec := harhar.NewRecorder()
	client := &http.Client{Transport: rec.Transport(nil)}
	do := func(req *http.Request) {
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	req, _ := http.NewRequest("GET", srv.URL+"/v1/items", nil)
	req.Host = "api.example.com"
	do(req)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("name", "widget")
	fw, _ := mw.CreateFormFile("file", "a.txt")
	io.WriteString(fw, "contents")
	mw.Close()
	req, _ = http.NewRequest("POST", srv.URL+"/v1/items", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "Bearer real")
	do(req)

	req, _ = http.NewRequest("GET", srv.URL+"/v1/text", nil)
	do(req)

	h := rec.HAR()
	for i := range h.Log.Entries {
		e := &h.Log.Entries[i]
		e.Request.URL = strings.Replace(e.Request.URL, srv.URL, "https://api.example.com", 1)
	}
	return writeHAR(t, h)
}

func writeHAR(t *testing.T, h *harhar.HAR) string {
	t.Helper()
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "recorded.har")
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func replay(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	t.Logf("exit %d\n%s%s", code, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestReplayAgainstMock(t *testing.T) {
	srv := httptest.NewServer(api(1))
	har := record(t, srv)
	srv.Close()

	m, err := mock.Load(har)
	if err != nil {
		t.Fatal(err)
	}
	mockSrv := httptest.NewServer(m.Middleware(nil))
	defer mockSrv.Close()

	// The mock rewrites api.example.com in the items' next link to its own
	// host, and harreplay allows for that, so nothing differs.
	code, out, stderr := replay(t, "-t", mockSrv.URL, "-headers", har)
	if code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	if !strings.Contains(out, "4 requests: 4 same, 0 different, 0 failed, 0 skipped") {
		t.Errorf("summary missing from\n%s", out)
	}
	if !strings.Contains(out, "ok    GET    "+mockSrv.URL+"/v1/items  200") {
		t.Errorf("ok line missing from\n%s", out)
	}
	if !strings.Contains(stderr, "Authorization (1)") {
		t.Errorf("no note about the redacted Authorization header in\n%s", stderr)
	}
}

func TestReplayFindsDifferences(t *testing.T) {
	srv := httptest.NewServer(api(1))
	har := record(t, srv)
	srv.Close()
	srv2 := httptest.NewServer(api(2))
	defer srv2.Close()

	code, out, _ := replay(t, "-t", srv2.URL, "-headers", har)
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	for _, want := range []string{
		"DIFF  GET    " + srv2.URL + "/v1/items  200",
		`body $.items[0].name: "widget" -> "gadget"`,
		`body $.items[0].updated: "1" -> "2"`,
		`header X-Version: "1" -> "2"`,
		"ok    POST   " + srv2.URL + "/v1/items  201", // the multipart form was rebuilt
		"DIFF  GET    " + srv2.URL + "/v1/me  200 -> 401",
		"status: 200 -> 401",
		`body line 2: "line two" -> "line 2"`,
		"4 requests: 1 same, 3 different, 0 failed, 0 skipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q", want)
		}
	}

	// Supplying the credential, and ignoring what changed, leaves only the
	// text difference.
	headers := filepath.Join(t.TempDir(), "headers.txt")
	os.WriteFile(headers, []byte("Authorization: Bearer real\n"), 0o600)
	out2 := filepath.Join(t.TempDir(), "replayed.har")
	code, out, stderr := replay(t, "-t", srv2.URL, "-h", headers, "-ignore-header", "X-Version",
		"-ignore-json", "name", "-ignore-json", "updated", "-q", "-o", out2, har)
	if code != 1 || !strings.Contains(out, "3 same, 1 different") || strings.Contains(out, "ok ") {
		t.Errorf("exit code %d, want 1 with only /v1/text differing and printed", code)
	}
	if strings.Contains(stderr, "redacted") {
		t.Errorf("noted redaction although -h supplied the header:\n%s", stderr)
	}
	data, err := os.ReadFile(out2)
	if err != nil {
		t.Fatal(err)
	}
	var h harhar.HAR
	json.Unmarshal(data, &h)
	if len(h.Log.Entries) != 4 {
		t.Fatalf("-o recorded %d entries, want 4", len(h.Log.Entries))
	}
	for _, hdr := range h.Log.Entries[2].Request.Headers {
		if hdr.Name == "Authorization" && strings.Contains(hdr.Value, "real") {
			t.Errorf("-o recorded Authorization as %q", hdr.Value)
		}
	}
}

func TestReplayFailuresAndSkips(t *testing.T) {
	srv := httptest.NewServer(api(1))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	truncated := harhar.Entry{Request: harhar.Request{
		Method: "POST", URL: srv.URL + "/v1/items", BodySize: 100,
		PostData: harhar.PostData{MIMEType: "text/plain", Text: "first part"},
	}}
	failedThen := harhar.Entry{
		Request:  harhar.Request{Method: "GET", URL: "http://127.0.0.1:1/down"},
		Response: harhar.Response{Comment: "connection refused"},
	}
	failedNow := harhar.Entry{
		Request:  harhar.Request{Method: "GET", URL: srv.URL + "/v1/text"},
		Response: harhar.Response{Comment: "connection refused"},
	}
	worked := harhar.Entry{
		Request:  harhar.Request{Method: "GET", URL: "http://127.0.0.1:1/v1/text"},
		Response: harhar.Response{Status: 200, Content: harhar.Content{Text: "line one\nline two\n", Size: 18}},
	}
	h := harhar.NewHAR("test", "1")
	h.Log.Entries = []harhar.Entry{truncated, failedThen, failedNow, worked}

	code, out, _ := replay(t, writeHAR(t, h))
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	for _, want := range []string{
		"SKIP  POST   http://" + host + "/v1/items  the request body was not fully recorded",
		"ok    GET    http://127.0.0.1:1/down  failed, as recorded",
		"DIFF  GET    http://" + host + "/v1/text  failed -> 200",
		"FAIL  GET    http://127.0.0.1:1/v1/text",
		"4 requests: 1 same, 1 different, 1 failed, 1 skipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q", want)
		}
	}
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"-t", "/relative", "x.har"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{filepath.Join(t.TempDir(), "missing.har")}, &stdout, &stderr); code != 1 {
		t.Errorf("missing file: exit code %d, want 1", code)
	}
}

func TestJSONDiff(t *testing.T) {
	r := &replayer{opts: options{ignoreJSON: map[string]bool{"ts": true}}}
	a, _ := parseJSON([]byte(`{"a":1,"b":[1,2,3],"c":{"d":"x"},"e":true,"gone":1,"ts":1,"odd key":1,"n":1.50}`))
	b, _ := parseJSON([]byte(`{"a":2,"b":[1,2],"c":"flat","e":true,"new":null,"ts":2,"odd key":2,"n":1.5}`))
	var diffs []string
	r.jsonDiff("$", a, b, &diffs)
	want := []string{
		`body $.a: 1 -> 2`,
		`body $.b: 3 items -> 2 items`,
		`body $.c: {"d":"x"} -> "flat"`,
		`body $.gone: removed (was 1)`,
		`body $.n: 1.50 -> 1.5`,
		`body $.new: added null`,
		`body $["odd key"]: 1 -> 2`,
	}
	if strings.Join(diffs, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(diffs, "\n"), strings.Join(want, "\n"))
	}
}

func TestTextDiff(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := textDiff([]byte("a\n"+long+"OLD"+long), []byte("a\n"+long+"NEW"+long+"\nmore"))
	want := []string{
		`body line 2: "...` + strings.Repeat("x", 30) + `OLD` + strings.Repeat("x", 47) + `..." -> "...` + strings.Repeat("x", 30) + `NEW` + strings.Repeat("x", 47) + `..."`,
		"body: 2 lines -> 3 lines",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRequestBody(t *testing.T) {
	body, ct, err := requestBody(harhar.Request{PostData: harhar.PostData{
		MIMEType: "application/x-www-form-urlencoded",
		Params:   []harhar.PostParam{{Name: "b", Value: "2 3"}, {Name: "a", Value: "&"}},
	}})
	if err != nil || string(body) != "b=2+3&a=%26" || ct != "" {
		t.Errorf("urlencoded: got %q, %q, %v", body, ct, err)
	}

	body, ct, err = requestBody(harhar.Request{PostData: harhar.PostData{
		MIMEType: "multipart/form-data; boundary=abc123",
		Params:   []harhar.PostParam{{Name: "f", FileName: `q"uote.txt`, ContentType: "text/plain", Value: "hi"}},
	}})
	if err != nil || ct != "multipart/form-data; boundary=abc123" {
		t.Fatalf("multipart: got %q, %v", ct, err)
	}
	req := &http.Request{Method: "POST", Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(bytes.NewReader(body))}
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	if fh := req.MultipartForm.File["f"]; len(fh) != 1 || fh[0].Filename != `q"uote.txt` {
		t.Errorf("multipart file = %+v", req.MultipartForm.File)
	}

	if _, _, err := requestBody(harhar.Request{BodySize: 10}); err != errTruncated {
		t.Errorf("unrecorded body: err = %v, want errTruncated", err)
	}
}

func TestNewRequestTarget(t *testing.T) {
	target, _ := url.Parse("http://127.0.0.1:9000/prefix/")
	r := newReplayer(http.DefaultClient, nil, options{target: target})
	req, err := r.newRequest(harhar.Entry{Request: harhar.Request{
		Method: "GET", URL: "https://user:xxxxx@api.example.com/a%2Fb?q=1#frag",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := req.URL.String(), "http://127.0.0.1:9000/prefix/a%2Fb?q=1"; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
	if r.redacted["URL password"] != 1 {
		t.Errorf("the redacted URL password wasn't noted")
	}
}

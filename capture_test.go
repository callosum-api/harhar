package harhar

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// echo writes the request body back.
func echo(w http.ResponseWriter, r *http.Request) { io.Copy(w, r.Body) }

func TestGetOutputMatchesSpec(t *testing.T) {
	srv, cli, rec := newTestClient(t, echo)
	req, _ := http.NewRequest("GET", srv.URL+"/p#frag", nil)
	req.AddCookie(&http.Cookie{Name: "s", Value: "v"})
	mustDo(t, cli, req)

	data, err := json.Marshal(rec.HAR())
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Log struct {
			Version string `json:"version"`
			Creator Creator
			Entries []struct {
				Request map[string]json.RawMessage `json:"request"`
			} `json:"entries"`
		} `json:"log"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Log.Version != "1.2" {
		t.Errorf("log.version = %q, want 1.2", raw.Log.Version)
	}
	if name := raw.Log.Creator.Name; strings.ContainsRune(name, filepath.Separator) {
		t.Errorf("creator name %q leaks a path", name)
	}
	request := raw.Log.Entries[0].Request
	if _, ok := request["postData"]; ok {
		t.Errorf("GET request has postData: %s", request["postData"])
	}
	if c := string(request["cookies"]); strings.Contains(c, "expires") {
		t.Errorf("cookie without expiry has expires: %s", c)
	}
	if u := string(request["url"]); u != `"`+srv.URL+`/p"` {
		t.Errorf("url = %s, want fragment removed", u)
	}
}

func TestRequestURLRedactsPassword(t *testing.T) {
	srv, cli, rec := newTestClient(t, echo)
	u, _ := url.Parse(srv.URL)
	u.User = url.UserPassword("user", "hunter2")
	req, _ := http.NewRequest("GET", u.String(), nil)
	mustDo(t, cli, req)
	if got := onlyEntry(t, rec).Request.URL; strings.Contains(got, "hunter2") {
		t.Errorf("url %q contains the password", got)
	}
}

func TestRoundTripDoesNotModifyRequest(t *testing.T) {
	srv, cli, _ := newTestClient(t, echo)
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("payload"))
	origBody := req.Body
	if got := string(mustDo(t, cli, req)); got != "payload" {
		t.Errorf("server got body %q", got)
	}
	if req.Body != origBody {
		t.Error("RoundTrip replaced the caller's req.Body")
	}
}

// A 307 redirect resends the body; both hops should record it.
func TestRedirectResendsBody(t *testing.T) {
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
			return
		}
		echo(w, r)
	})
	req, _ := http.NewRequest("POST", srv.URL+"/old", strings.NewReader("payload"))
	if got := string(mustDo(t, cli, req)); got != "payload" {
		t.Errorf("final body %q", got)
	}
	for i, ent := range rec.HAR().Log.Entries {
		if ent.Request.PostData.Text != "payload" {
			t.Errorf("entry %d postData = %+v", i, ent.Request.PostData)
		}
	}
	if ent := rec.HAR().Log.Entries[0]; ent.Response.RedirectURL != srv.URL+"/new" {
		t.Errorf("redirectURL = %q", ent.Response.RedirectURL)
	}
}

func TestPostDataURLEncoded(t *testing.T) {
	srv, cli, rec := newTestClient(t, echo)
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("b=2&a=1&b=x+y%21"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	mustDo(t, cli, req)

	pd := onlyEntry(t, rec).Request.PostData
	want := []PostParam{{Name: "b", Value: "2"}, {Name: "a", Value: "1"}, {Name: "b", Value: "x y!"}}
	if !slices.Equal(pd.Params, want) {
		t.Errorf("params = %+v, want %+v", pd.Params, want)
	}
	if pd.Text != "" {
		t.Errorf("text = %q, want empty alongside params", pd.Text)
	}
}

func TestPostDataMultipart(t *testing.T) {
	srv, cli, rec := newTestClient(t, echo)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("field", "value")
	fw, _ := mw.CreateFormFile("upload", "notes.txt")
	io.WriteString(fw, "file contents")
	mw.Close()

	req, _ := http.NewRequest("POST", srv.URL, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	mustDo(t, cli, req)

	want := []PostParam{
		{Name: "field", Value: "value"},
		{Name: "upload", Value: "file contents", FileName: "notes.txt", ContentType: "application/octet-stream"},
	}
	if got := onlyEntry(t, rec).Request.PostData.Params; !slices.Equal(got, want) {
		t.Errorf("params = %+v, want %+v", got, want)
	}
}

func TestPostDataMalformedMultipartFallsBackToText(t *testing.T) {
	srv, cli, rec := newTestClient(t, echo)
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
	mustDo(t, cli, req)
	if pd := onlyEntry(t, rec).Request.PostData; pd.Text != "not multipart" || pd.Params != nil {
		t.Errorf("postData = %+v", pd)
	}
}

func TestBinaryResponseIsBase64(t *testing.T) {
	bin := []byte{0xff, 0xfe, 0x00, 0x01}
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(bin)
	})
	req, _ := http.NewRequest("GET", srv.URL, nil)
	if got := mustDo(t, cli, req); !bytes.Equal(got, bin) {
		t.Errorf("caller got %x", got)
	}
	content := onlyEntry(t, rec).Response.Content
	if content.Encoding != "base64" || content.Text != "//4AAQ==" {
		t.Errorf("content = %+v", content)
	}
}

func TestGzipResponseBodySizeUnknown(t *testing.T) {
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		io.WriteString(zw, "hello")
		zw.Close()
	})
	req, _ := http.NewRequest("GET", srv.URL, nil)
	mustDo(t, cli, req)
	resp := onlyEntry(t, rec).Response
	if resp.BodySize != -1 || resp.Content.Size != 5 || resp.Content.Text != "hello" {
		t.Errorf("bodySize = %d, content = %+v", resp.BodySize, resp.Content)
	}
}

func TestFailedRequestIsRecorded(t *testing.T) {
	// Reserve a port, then close it so the connection is refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	rec := NewRecorder()
	_, err = (&http.Client{Transport: rec.Transport(nil)}).Get("http://" + addr)
	if err == nil {
		t.Fatal("expected an error")
	}
	resp := onlyEntry(t, rec).Response
	if resp.Status != 0 || resp.Comment == "" {
		t.Errorf("response = %+v", resp)
	}
}

func TestResponseReadErrorReachesCaller(t *testing.T) {
	srv, cli, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		io.WriteString(w, "abc") // short body; the server closes the connection
	})
	resp, err := cli.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "abc" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("caller got %q, %v; want \"abc\", unexpected EOF", body, err)
	}
	if ent := onlyEntry(t, rec); ent.Response.Content.Text != "abc" || ent.Response.Comment == "" {
		t.Errorf("response = %+v", ent.Response)
	}
}

func TestMiddlewareRequestCapture(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// The handler still sees the body the recorder read.
		r.ParseForm()
		io.WriteString(w, r.PostForm.Get("a"))
	})
	resp, err := http.Post(srv.URL+"/form?q=1", "application/x-www-form-urlencoded", strings.NewReader("a=hello"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello" {
		t.Errorf("handler saw form value %q", body)
	}
	req := onlyEntry(t, rec).Request
	if req.URL != srv.URL+"/form?q=1" {
		t.Errorf("url = %q, want absolute", req.URL)
	}
	if len(req.PostData.Params) != 1 || req.PostData.Params[0].Value != "hello" {
		t.Errorf("postData = %+v", req.PostData)
	}
}

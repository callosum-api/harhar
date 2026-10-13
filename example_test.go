package harhar_test

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/callosum-api/harhar"
)

func Example() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello")
	}))
	defer srv.Close()

	rec := harhar.NewRecorder()
	client := &http.Client{Transport: rec.Transport(nil)}

	resp, err := client.Get(srv.URL)
	if err != nil {
		log.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close() // the entry is recorded once the body is done

	for _, e := range rec.HAR().Log.Entries {
		fmt.Println(e.Request.Method, e.Response.Status, e.Response.Content.Text)
	}
	// Then save it with rec.WriteFile("out.har").

	// Output: GET 200 hello
}

func ExampleRecorder_Middleware() {
	rec := harhar.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /items", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "created "+r.PostForm.Get("name"))
	})
	srv := httptest.NewServer(rec.Middleware(mux))

	resp, err := http.PostForm(srv.URL+"/items", map[string][]string{"name": {"widget"}})
	if err != nil {
		log.Fatal(err)
	}
	resp.Body.Close()
	srv.Close() // waits for the handler, and so the entry, to finish

	e := rec.HAR().Log.Entries[0]
	fmt.Println(e.Request.Method, e.Request.PostData.Params[0].Value)
	fmt.Println(e.Response.Status, e.Response.Content.Text)

	// Output:
	// POST widget
	// 201 created widget
}

func ExampleRecorder_Transport() {
	rec := harhar.NewRecorder()

	// Record through a customized transport, here one limited to HTTP/1.1.
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Protocols = new(http.Protocols)
	base.Protocols.SetHTTP1(true)

	client := &http.Client{Transport: rec.Transport(base)}
	_ = client
}

func ExampleNewRecorder() {
	rec := harhar.NewRecorder(
		harhar.WithCreator("myapp", "1.2.3"),
		harhar.WithMaxBodySize(64<<10),
		// Keep only the latest 1000 entries in memory.
		harhar.WithMaxEntries(1000),
	)
	_ = rec
}

func ExampleWithHeaderRedactor() {
	rec := harhar.NewRecorder(harhar.WithHeaderRedactor(func(name, value string) string {
		switch name {
		case "X-Api-Key":
			return harhar.RedactedValue // a custom credential header
		case "Cookie":
			return value // this app's cookies aren't sensitive
		}
		return harhar.DefaultHeaderRedactor(name, value)
	}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer abc123")
	req.Header.Set("X-Api-Key", "k-456")
	req.Header.Set("Cookie", "theme=dark")
	resp, err := (&http.Client{Transport: rec.Transport(nil)}).Do(req)
	if err != nil {
		log.Fatal(err)
	}
	resp.Body.Close()

	for _, h := range rec.HAR().Log.Entries[0].Request.Headers {
		fmt.Printf("%s: %s\n", h.Name, h.Value)
	}
	// Output:
	// Authorization: Bearer [REDACTED]
	// Cookie: theme=dark
	// X-Api-Key: [REDACTED]
}

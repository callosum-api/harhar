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
		// Also redact a custom credential header.
		harhar.WithRedactedHeaders(append(harhar.DefaultRedactedHeaders(), "X-Api-Key")...),
		harhar.WithMaxBodySize(64<<10),
		// Keep only the latest 1000 entries in memory.
		harhar.WithMaxEntries(1000),
	)
	_ = rec
}

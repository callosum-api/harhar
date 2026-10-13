package mock_test

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/callosum-api/harhar"
	"github.com/callosum-api/harhar/mock"
)

// record returns a HAR log of one request to https://api.example.com. A
// real program would load one with mock.Load instead.
func record() *harhar.HAR {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"name":"widget","next":"https://api.example.com/items/2"}`)
	}))
	defer srv.Close()

	rec := harhar.NewRecorder()
	client := &http.Client{Transport: rec.Transport(nil)}
	resp, err := client.Get(srv.URL + "/items/1")
	if err != nil {
		log.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	h := rec.HAR()
	h.Log.Entries[0].Request.URL = "https://api.example.com/items/1"
	return h
}

func ExampleMock_Transport() {
	m := mock.New(record()) // or mock.Load("recorded.har")

	// The client gets recorded responses without touching the network.
	client := &http.Client{Transport: m.Transport(nil)}
	resp, err := client.Get("https://api.example.com/items/1")
	if err != nil {
		log.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Println(resp.StatusCode, string(body))

	// Output: 200 {"name":"widget","next":"https://api.example.com/items/2"}
}

func ExampleMock_Middleware() {
	m := mock.New(record())

	// A fake server that answers for api.example.com.
	srv := httptest.NewServer(m.Middleware(nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/items/1")
	if err != nil {
		log.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Links to the recorded host now point at the fake server.
	fmt.Println(string(body) == `{"name":"widget","next":"`+srv.URL+`/items/2"}`)

	// Output: true
}

func ExampleWithHost() {
	// Answer requests for a local host with what api.example.com returned.
	m := mock.New(record(), mock.WithHost("api.example.com", "localhost:8080"))

	client := &http.Client{Transport: m.Transport(nil)}
	resp, err := client.Get("http://localhost:8080/items/1")
	if err != nil {
		log.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Println(string(body))

	// Output: {"name":"widget","next":"http://localhost:8080/items/2"}
}

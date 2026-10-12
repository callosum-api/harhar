harhar
======

HTTP Archive (HAR) recording for Go's `net/http`, on both the client side
(`http.RoundTripper`) and the server side (`http.Handler`).

Getting Started
---------------

For logging from an `http.Client`, set the recorder as its Transport:

```go
recorder := harhar.NewRecorder()
client := &http.Client{
	Transport: recorder,
}
```

To wrap a different upstream transport, replace `recorder.RoundTripper`
before making any requests.

For logging requests handled by a server, set `recorder.Handler` to your
handler and serve the recorder itself:

```go
recorder := harhar.NewRecorder()
recorder.Handler = mux
http.ListenAndServe("localhost:8080", recorder)
```

Then, whenever you're ready to generate the HAR output, call WriteFile:

```go
recorder.WriteFile("output.har")
```

Commands
--------

- `cmd/harhar` fetches URLs and saves the results: `harhar -o out.har https://example.com`
- `cmd/harprox` is a recording reverse proxy: `harprox -p https://api.example.com -o out.har`

Security
--------

HAR files contain everything that was sent and received, including
`Authorization` headers, cookies, and request and response bodies. Treat
them like credentials.

Request and response bodies are held in memory in full, and every entry is
kept until the program exits. Don't leave a recorder attached to
long-running or high-volume traffic.

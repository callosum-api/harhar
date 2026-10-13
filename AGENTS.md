# AGENTS.md

Guidance for coding agents using `github.com/callosum-api/harhar` from other
code, plus a short section for working on this repo. Humans should start with
[README.md](README.md). Code written for the old API needs
[MIGRATION.md](MIGRATION.md).

## What it is

harhar records `net/http` traffic as HTTP Archive (HAR 1.2) files. A
`*Recorder` stores entries. You attach it to an `http.Client` with
`rec.Transport(base)`, to an `http.Handler` with `rec.Middleware(next)`, or to
both. It has no dependencies outside the standard library and needs Go 1.24+.

## When to use it

Good fits:

- Capturing what an API client actually sent and received, for debugging or
  for attaching to a bug report. Open the file in browser dev tools or any HAR
  viewer.
- Logging the requests a Go HTTP server handles, including bodies and timings.
- Recording traffic in tests and asserting on it via `rec.HAR().Log.Entries`.
- Recording a third-party API without changing any code: run the `harprox`
  reverse proxy (see [Commands](#commands)).
- Reading existing `.har` files. The exported types (`HAR`, `Entry`,
  `Request`, ...) follow the spec and can be decoded with `encoding/json`.

Not a fit:

- Replaying or mocking HTTP. harhar only records; it has no playback.
- Exact wire capture. On the server side, headers that `net/http` adds after
  the handler runs (`Date`, `Content-Length`) are not recorded. Response text
  is not transcoded to UTF-8.
- Browser page grouping. `Log.Pages` and `Entry.PageRef` are never filled in.
- Non-`net/http` traffic (gRPC over a custom transport, raw TCP, WebSockets
  after the upgrade).

## Usage

```go
import "github.com/callosum-api/harhar"

rec := harhar.NewRecorder() // options below

// Client side. nil wraps http.DefaultTransport. Pass your own transport to
// keep its settings (proxies, TLS config, HTTP/2 off, ...).
client := &http.Client{Transport: rec.Transport(nil)}

// Server side.
http.ListenAndServe(addr, rec.Middleware(mux))

// Save at any time, including while requests are in flight.
rec.WriteFile("out.har") // atomic replace, mode 0600
rec.WriteTo(w)           // JSON to any io.Writer
h := rec.HAR()           // *harhar.HAR snapshot, oldest entry first
rec.Reset()              // drop everything recorded so far
```

One `Recorder` can serve any number of clients and handlers at once. It is
safe for concurrent use. Pass it around as a `*Recorder` and never copy the
value.

### Options

| Option | Default | Use it when |
|---|---|---|
| `WithCreator(name, version)` | `"harhar"` at the module version | The file should name your app. |
| `WithMaxBodySize(n)` | `DefaultMaxBodySize` (32 MiB) | You want smaller files (`64<<10`), no bodies (`0`), or full bodies (`-1`). Only the recorded copy is truncated, never the real traffic. |
| `WithMaxEntries(n)` | unlimited | The process is long-running. It keeps only the latest `n` entries, and `0` keeps none. |
| `WithOnEntry(fn)` | none | You want to stream entries to a log, queue or file instead of holding them in memory. Pair it with `WithMaxEntries(0)`. |
| `WithHeaderRedactor(fn)` | `DefaultHeaderRedactor` | You need to redact more headers, or fewer. `nil` disables redaction. |

To add redaction, wrap the default instead of replacing it:

```go
harhar.WithHeaderRedactor(func(name, value string) string {
	if name == "X-Api-Key" { // name is canonical, e.g. "X-Api-Key"
		return harhar.RedactedValue
	}
	return harhar.DefaultHeaderRedactor(name, value)
})
```

## Rules that are easy to get wrong

1. **Client entries are recorded only when the response body is finished.**
   That means read to EOF or closed. Always `defer resp.Body.Close()`. A
   response whose body is never closed is never recorded, and it leaks a
   connection anyway. Responses with no body (`HEAD`, `204`) are recorded
   immediately.
2. **Server entries are recorded only after the handler returns.** A client
   can receive the full response before the entry exists. With `httptest`,
   call `srv.Close()` (which waits for handlers) before reading `rec.HAR()`.
3. **Where you put the middleware decides what it sees.** Wrap the outermost
   handler to record everything. If you put it inside auth or routing
   middleware, the requests those reject are never recorded.
4. **Don't modify what `HAR()` returns, or the `Entry` passed to the
   `WithOnEntry` callback.** Entries share memory with the Recorder, so
   deep-copy anything you need to change.
5. **`WithOnEntry` and custom redactors run inline on request goroutines,
   possibly concurrently.** Keep them fast and thread-safe. A slow callback
   delays the `Body.Close()` or handler return that triggered it.
6. **Memory grows without bound by default.** Every entry, with up to 32 MiB
   per body, is kept until `Reset`. Anything long-running needs
   `WithMaxEntries`, `WithOnEntry`, a smaller `WithMaxBodySize`, or periodic
   `WriteFile` followed by `Reset`.
7. **Treat HAR files as secrets.** Only `Authorization`,
   `Proxy-Authorization`, `Cookie` and `Set-Cookie` are redacted by default,
   and passwords in URLs are masked. Bodies, query strings, and custom
   credential headers such as `X-Api-Key` are recorded as-is. Don't commit HAR
   files or log them without redacting further.

## Reading recorded entries

- `Response.Status == 0` means the request failed with no response. The error
  text is in `Response.Comment`.
- `Response.Content.Text` is base64 when `Content.Encoding == "base64"`, which
  happens for any body that isn't valid UTF-8. Check before using it.
- A truncated body has a `Comment` like `body truncated: recorded N of M
  bytes`. `Content.Size` and `Request.BodySize` are always the full size.
- `Response.BodySize == -1` means `net/http` transparently decompressed the
  body, so the on-wire size is unknown. `Content.Size` is the decompressed
  size.
- Request bodies go in `Request.PostData`. Form bodies (urlencoded and
  multipart) are parsed into `Params`. Everything else goes in `Text`, and the
  two are never both set.
- Timings are `float64` milliseconds. `Blocked`, `DNS`, `Connect` and `SSL`
  are `-1` when they don't apply, so skip negatives when summing. `SSL` is
  already included in `Connect`. Server-side entries have only `Wait` and
  `Receive`.
- Headers are sorted by name and appear in their redacted form. Cookie lists
  are parsed from the redacted headers.

## Testing with harhar

```go
rec := harhar.NewRecorder()
client := &http.Client{Transport: rec.Transport(nil)}

resp, err := client.Get(srv.URL + "/thing")
if err != nil {
	t.Fatal(err)
}
io.Copy(io.Discard, resp.Body)
resp.Body.Close() // the entry is recorded here

e := rec.HAR().Log.Entries[0]
// assert on e.Request.Method, e.Request.Headers, e.Response.Status, e.Response.Content.Text ...
```

For server-side tests, use `httptest.NewServer(rec.Middleware(h))` and call
`srv.Close()` before inspecting entries. [example_test.go](example_test.go)
has runnable versions of both.

## Commands

No code required:

```bash
go run github.com/callosum-api/harhar/cmd/harhar@latest -o out.har https://example.com
```

```bash
go run github.com/callosum-api/harhar/cmd/harprox@latest -p https://api.example.com -o out.har
```

- `harhar` GETs each URL and writes one HAR file. It keeps going after a
  failure, records the failure, and exits 1 at the end.
- `harprox` forwards `http://127.0.0.1:6060/<path>` to `-p` + `<path>`. It
  saves every 5s while there is new traffic (`-n`) and again on exit. `-h
  file` injects `Name: value` headers, such as an API token, into every
  proxied request; those headers are fully redacted in the HAR file. `-s`
  records on the server side instead, which gives less timing detail. It
  listens only on loopback unless `-i` says otherwise. It does not follow
  redirects; it passes them back to the client.

Both accept `-max-body n`. Run either one with `-help` to see every flag.

## Working on this repo

- Layout: [recorder.go](recorder.go) (store and output),
  [transport.go](transport.go) (client side and timings),
  [middleware.go](middleware.go) (server side),
  [capture.go](capture.go) (body capture and HAR conversion),
  [redact.go](redact.go), [options.go](options.go),
  [structs.go](structs.go) (HAR 1.2 types), and `cmd/` (CLIs).
- CI runs `go vet ./...` and `go test -race ./...` on Go 1.24 and on stable,
  plus `staticcheck` and `govulncheck`. Run the same before proposing changes.
  Set `GOTOOLCHAIN=local` to test against your installed Go instead of the
  toolchain pinned in `go.mod`.
- Use only the standard library. Keep `go.mod`'s `go 1.24` floor; don't use
  APIs from newer releases.
- Struct field names and JSON tags follow the HAR 1.2 spec. Breaking API
  changes need an entry in [MIGRATION.md](MIGRATION.md). User-visible
  behavior belongs in [README.md](README.md), and this file should be updated
  to match.

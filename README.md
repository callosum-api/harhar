harhar
======

HTTP Archive (HAR) recording for Go's `net/http`, on both the client side
(`http.RoundTripper`) and the server side (`http.Handler`). The resulting
`.har` files open in browser dev tools and other HAR viewers.

```
go get github.com/callosum-api/harhar
```

Upgrading from the original API? See [MIGRATION.md](MIGRATION.md).

Getting Started
---------------

Create a `Recorder`, then attach it to clients, servers, or both.

To record the requests an `http.Client` makes, use `Transport`:

```go
rec := harhar.NewRecorder()
client := &http.Client{Transport: rec.Transport(nil)} // nil wraps http.DefaultTransport
```

Entries are recorded as response bodies finish, so read or close every
response body, as `net/http` already requires.

To record the requests a server handles, use `Middleware`:

```go
rec := harhar.NewRecorder()
http.ListenAndServe("localhost:8080", rec.Middleware(mux))
```

Then save the log whenever you like. Both calls are safe while requests
are in flight:

```go
rec.WriteFile("output.har") // or rec.WriteTo(w), or rec.HAR() for the data
```

Options
-------

```go
rec := harhar.NewRecorder(
	harhar.WithCreator("myapp", "1.2.3"),
	harhar.WithMaxBodySize(64<<10), // bytes recorded per body; -1 for no limit
	harhar.WithMaxEntries(1000),    // keep only the latest entries
	harhar.WithOnEntry(func(e harhar.Entry) { /* stream it somewhere */ }),
)
```

### Redaction

Header values pass through a redaction function before they're recorded.
The default, `harhar.DefaultHeaderRedactor`, replaces only the secret
part of credential headers and keeps the rest for context:

| Header | Sent | Recorded |
|---|---|---|
| `Authorization`, `Proxy-Authorization` | `Bearer abc123` | `Bearer [REDACTED]` |
| `Cookie` | `id=abc; theme=dark` | `id=[REDACTED]; theme=[REDACTED]` |
| `Set-Cookie` | `id=abc; Path=/; HttpOnly` | `id=[REDACTED]; Path=/; HttpOnly` |

The recorded cookie lists are parsed from the redacted headers, so they
always match. To change what's redacted, supply your own function and fall
back to the default:

```go
harhar.WithHeaderRedactor(func(name, value string) string {
	switch name {
	case "X-Api-Key":
		return harhar.RedactedValue // also redact this header
	case "Cookie":
		return value // these cookies aren't sensitive
	}
	return harhar.DefaultHeaderRedactor(name, value)
})
```

`WithHeaderRedactor(nil)` records every header as is.

Mocking
-------

The `mock` package answers requests with the responses in a HAR file, so
tests can run against a recording instead of a live API:

```go
import "github.com/callosum-api/harhar/mock"

m, err := mock.Load("recorded.har") // or mock.New(rec.HAR())

client := &http.Client{Transport: m.Transport(nil)} // client side, no network
srv := httptest.NewServer(m.Middleware(nil))        // or a fake server
```

Requests match recorded entries by method, path and query. Repeated
requests get the matching responses in the order they were recorded, and
the last one repeats. Requests that match nothing go to the fallback you
pass. With `nil`, the transport returns `mock.ErrNoMatch` and the server
returns a 404.

The fake server answers for every host in the recording. In response
headers and text bodies, it rewrites URLs that point to a recorded host so
they point to the fake server, so redirects and links keep working. On the
client side, `mock.WithHost("api.example.com", "localhost:8080")` serves a
recording made against one host for requests to another, rewriting URLs to
match. Use `mock.WithMatcher` to change how requests are matched.

Commands
--------

`harhar` fetches URLs and saves the results:

```bash
go run github.com/callosum-api/harhar/cmd/harhar@latest -o out.har https://example.com
```

`harprox` is a reverse proxy that records everything passing through it.
Requests to `http://127.0.0.1:6060/<path>` are forwarded to the `-p` URL
plus `<path>`:

```bash
go run github.com/callosum-api/harhar/cmd/harprox@latest -p https://api.example.com -o out.har
```

It saves the file every 5 seconds while there's new traffic (`-n`) and
again on Ctrl-C. `-h headers.txt` adds `Name: value` headers to every
proxied request, such as an API token; those headers are redacted in the
HAR file. It listens only on localhost unless you pass `-i`.

`harmock` is a mock server that answers with the responses in HAR files. It
listens on the same address as `harprox` by default. A client set up to
record through `harprox` can therefore run against the recording unchanged:

```bash
go run github.com/callosum-api/harhar/cmd/harmock@latest out.har
```

On startup it lists every route it answers, with the recorded statuses in
order, and the settings in effect. It then logs each request and how it was
answered. `-ignore-query name` ignores a parameter such as a cache buster
when matching. `-fallback URL` forwards unmatched requests to a real server
instead of returning a 404. `-host recorded=host` rewrites one recorded host
to some other host instead of to the mock.

`harreplay` sends the recorded requests again and compares each new response
with the recorded one:

```bash
go run github.com/callosum-api/harhar/cmd/harreplay@latest -t https://staging.example.com out.har
```

```
ok    GET    https://staging.example.com/v1/items  200
DIFF  GET    https://staging.example.com/v1/me  200 -> 401
        status: 200 -> 401
        body line 1: "it's you" -> "unauthorized"
```

Status, `Content-Type` and body are always compared. `-headers` adds the
other response headers, except ones such as `Date` that change on every
response. JSON bodies are compared value by value, and `-ignore-json key`
skips a member such as a timestamp. URLs naming the recorded hosts are
rewritten to the `-t` target before comparing. Credentials redacted in the
recording aren't sent, so supply them with `-h headers.txt`. `-o` records
the new traffic. The exit status is 1 if anything differed or failed.

Run any command with `-help` for all flags.

Security
--------

HAR files contain the traffic as sent and received. By default the secrets
in `Authorization`, `Proxy-Authorization`, `Cookie` and `Set-Cookie` headers
are redacted (see [Redaction](#redaction)), but URLs, bodies and all other
headers are recorded as is. Treat HAR files like credentials. `WriteFile`
creates files readable only by the current user.

Each body is recorded up to 32 MiB by default, and every entry is kept in
memory until you call `Reset`. For long-running programs, use
`WithMaxEntries` or `WithOnEntry`.

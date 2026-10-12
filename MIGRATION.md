Migrating to the new API
========================

The `Recorder` API was redesigned: a `Recorder` is now only a store of
entries, and you attach it to clients with `Recorder.Transport` and to
servers with `Recorder.Middleware`. The HAR types were also renamed to match
the HAR 1.2 spec. Old code will not compile against the new API, so the
compiler will point you at everything that needs changing. This guide
covers how to change each part.

Recorder
--------

### Client side

```go
// Before
rec := harhar.NewRecorder()
client := &http.Client{Transport: rec}

// After
rec := harhar.NewRecorder()
client := &http.Client{Transport: rec.Transport(nil)}
```

To wrap your own transport, pass it to `Transport` instead of setting the
`RoundTripper` field:

```go
// Before
rec.RoundTripper = myTransport

// After
client := &http.Client{Transport: rec.Transport(myTransport)}
```

**Read or close every response body.** Bodies are now recorded as they
stream through rather than read up front, so an entry is recorded when its
body reaches EOF or is closed. Code that drops response bodies without
closing them (already a leak in net/http) won't get entries for them.
Responses without a body, such as `HEAD` requests, are recorded right away.

### Disabling HTTP/2

`DisableHTTP2` is gone. Configure the protocols on the transport you pass in:

```go
// Before
rec.DisableHTTP2(true)

// After
base := http.DefaultTransport.(*http.Transport).Clone()
base.Protocols = new(http.Protocols)
base.Protocols.SetHTTP1(true)
client := &http.Client{Transport: rec.Transport(base)}
```

### Server side

```go
// Before
rec := harhar.NewRecorder()
rec.Handler = mux
http.ListenAndServe(addr, rec)

// After
rec := harhar.NewRecorder()
http.ListenAndServe(addr, rec.Middleware(mux))
```

If you relied on the old default of `http.DefaultServeMux`, wrap it
explicitly: `rec.Middleware(http.DefaultServeMux)`.

`HARResponseWriter` was removed. The middleware now passes responses
through as they're written instead of buffering them, so streaming, `Flush`,
`Hijack` and `http.ResponseController` work. The request body is recorded as
far as the handler reads it.

### Reading and saving the log

The `HAR` field is now a method that returns a snapshot, safe to call while
requests are in flight:

| Before | After |
|---|---|
| `rec.HAR.Log.Entries` | `rec.HAR().Log.Entries` |
| `rec.HAR.Log.Entries = nil` | `rec.Reset()` |
| `json.Marshal(rec.HAR)` | `rec.WriteTo(w)`, or `json.Marshal(rec.HAR())` |
| `n, err := rec.WriteFile(name)` (`int`) | `n, err := rec.WriteFile(name)` (`int64`) |
| `rec.HAR.Log.Creator = ...` | `harhar.NewRecorder(harhar.WithCreator(name, version))` |

`WriteFile` now replaces the file atomically and creates it with mode 0600,
since HAR files hold sensitive data. `NewHAR(name)` is now
`NewHAR(name, version)`.

New defaults
------------

These change what gets recorded. Each can be turned off with an option.

| Change | To get the old behavior |
|---|---|
| `Authorization`, `Cookie`, `Proxy-Authorization` and `Set-Cookie` values, and cookie values, are recorded as `[REDACTED]` | `harhar.WithRedactedHeaders()` |
| Bodies are recorded up to 1 MiB, with a comment noting the truncation | `harhar.WithMaxBodySize(-1)` |
| The creator is `harhar` at its module version, not the program name | `harhar.WithCreator(name, version)` |

To redact more headers, extend the defaults:

```go
harhar.WithRedactedHeaders(append(harhar.DefaultRedactedHeaders(), "X-Api-Key")...)
```

For long-running programs, `harhar.WithMaxEntries(n)` keeps only the latest
`n` entries, and `harhar.WithOnEntry(fn)` hands each entry to you as it's
recorded.

HAR types
---------

### Renamed types

These can be rewritten automatically:

```bash
gofmt -w -r 'harhar.BodyType -> harhar.PostData' .
gofmt -w -r 'harhar.BodyResponseType -> harhar.Content' .
gofmt -w -r 'harhar.PostNameValuePair -> harhar.PostParam' .
gofmt -w -r 'harhar.CacheState -> harhar.Cache' .
gofmt -w -r 'harhar.PageTiming -> harhar.PageTimings' .
```

### Renamed fields

| Before | After |
|---|---|
| `Request.Body` | `Request.PostData` |
| `Request.QueryParams` | `Request.QueryString` |
| `PostData.Content` (was `BodyType.Content`) | `PostData.Text` |
| `Response.StatusCode` | `Response.Status` |
| `Response.Body` | `Response.Content` |
| `Content.Content` (was `BodyResponseType.Content`) | `Content.Text` |
| `Entry.Start`, `Page.Start` | `Entry.StartedDateTime`, `Page.StartedDateTime` |

### Changed field types

| Field | Before | After |
|---|---|---|
| `Entry.StartedDateTime`, `Page.StartedDateTime` | `string` | `time.Time` |
| `Cookie.Expires`, `CacheInfo.Expires`, `CacheInfo.LastAccess` | `string` | `time.Time` (zero when unset) |
| `Entry.Time`, all `Timings` fields, `PageTimings` fields | `int` ms | `float64` ms, with sub-millisecond precision |
| `HeadersSize`, `BodySize`, `Content.Size`, `Content.Compression` | `int` | `int64` |

`Timings.Blocked`, `DNS`, `Connect` and `SSL` are now `-1` when they don't
apply (for example on a reused connection), as the spec requires, instead of
being omitted. If you sum timings, skip negative values.

### JSON output

The JSON is now closer to the HAR 1.2 spec, which may matter if you
post-process HAR files:

- `log.version` is `"1.2"`.
- Timings that don't apply are written as `-1`.
- `cache.*.etag` is now `eTag`, as in the spec.
- `log.entries` is always an array, never `null`.
- Response bodies that aren't valid UTF-8 are base64-encoded, with
  `content.encoding` set to `"base64"`.
- Requests that fail without a response are recorded with status `0` and the
  error in `response.comment`.

Commands
--------

`harprox`:

- Listens on `127.0.0.1:6060` by default instead of all interfaces. Pass
  `-i :6060` for the old behavior (it logs a warning).
- Passes redirects back to the client instead of following them, since it is
  now built on `httputil.ReverseProxy`.
- `-n` takes a duration such as `30s`. A bare number still means seconds.
- Headers from `-h` are redacted in the HAR file.
- Saves the HAR file once more on exit.

`harhar` keeps going after a failed URL, records the failure, and exits
with status 1 at the end, instead of stopping at the first failure.

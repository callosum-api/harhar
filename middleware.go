package harhar

import (
	"bufio"
	"net"
	"net/http"
	"time"
)

// Middleware returns an http.Handler that records each request served by
// next.
//
// Responses pass straight through as next writes them, so streaming
// responses, http.Flusher, http.Hijacker and http.ResponseController keep
// working. The request body is recorded as far as next reads it.
//
// Headers that the server adds after the handler runs, such as Date and
// Content-Length, are not recorded.
func (r *Recorder) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cfg := &r.cfg
		start := time.Now()
		ent := Entry{StartedDateTime: start, Request: cfg.makeRequest(req)}
		if addr, ok := req.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			ent.ServerIP = addrIP(addr)
		}

		reqBody := newBodyCapture(cfg.maxBodySize)
		if req.Body != nil && req.Body != http.NoBody {
			// Pass next a copy, as middleware shouldn't modify the request.
			req = req.WithContext(req.Context())
			req.Body = reqBody.wrap(req.Body, nil)
		}
		rw := &responseRecorder{w: w, body: newBodyCapture(cfg.maxBodySize)}

		next.ServeHTTP(rw, req)
		end := time.Now()
		if rw.status == 0 && !rw.hijacked {
			// A handler that writes nothing sends 200 OK.
			rw.recordHeader(http.StatusOK)
		}

		cfg.setPostData(&ent.Request, req.Header.Get("Content-Type"), reqBody)
		ent.Response = cfg.makeResponse(&http.Response{
			StatusCode: rw.status,
			Proto:      req.Proto,
			Header:     rw.header,
			Request:    req,
		})
		ent.Response.Content = cfg.makeContent(rw.header, rw.body)
		ent.Response.BodySize = ent.Response.Content.Size
		if rw.hijacked {
			ent.Response.Comment = "connection hijacked by handler"
		}

		// From the server's side, wait is the time until the response
		// started, and receive is the time spent writing it.
		firstByte := rw.firstByte
		if firstByte.IsZero() {
			firstByte = end
		}
		ent.Timings = Timings{
			Blocked: -1, DNS: -1, Connect: -1, SSL: -1,
			Wait:    msBetween(start, firstByte),
			Receive: msBetween(firstByte, end),
		}
		ent.Time = ent.Timings.total()
		r.add(ent)
	})
}

// responseRecorder passes a response through to w while recording it.
type responseRecorder struct {
	w    http.ResponseWriter
	body *bodyCapture

	status    int
	header    http.Header // snapshot taken when the status was written
	firstByte time.Time
	hijacked  bool
}

func (rw *responseRecorder) Header() http.Header { return rw.w.Header() }

func (rw *responseRecorder) WriteHeader(code int) {
	// Informational responses are sent, but only the final one is recorded.
	if code < 100 || code > 199 || code == http.StatusSwitchingProtocols {
		rw.recordHeader(code)
	}
	rw.w.WriteHeader(code)
}

// recordHeader records the status and headers, unless already recorded.
func (rw *responseRecorder) recordHeader(code int) {
	if rw.status != 0 {
		return
	}
	rw.status = code
	rw.header = rw.w.Header().Clone()
	rw.firstByte = time.Now()
}

func (rw *responseRecorder) Write(p []byte) (int, error) {
	rw.recordHeader(http.StatusOK)
	n, err := rw.w.Write(p)
	rw.body.Write(p[:n])
	return n, err
}

// Flush implements http.Flusher.
func (rw *responseRecorder) Flush() {
	rw.recordHeader(http.StatusOK)
	http.NewResponseController(rw.w).Flush()
}

// Hijack implements http.Hijacker. It returns an error wrapping
// http.ErrNotSupported if the underlying ResponseWriter can't be hijacked.
func (rw *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(rw.w).Hijack()
	if err == nil {
		rw.hijacked = true
	}
	return conn, brw, err
}

// Unwrap lets http.ResponseController reach the underlying ResponseWriter.
func (rw *responseRecorder) Unwrap() http.ResponseWriter { return rw.w }

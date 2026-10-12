package harhar

import (
	"bytes"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"time"
)

// ServeHTTP implements http.Handler (aka a Server-side recorder)
func (c *Recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := readBody(req.Body)
	if err != nil {
		log.Printf("harhar: reading request body for %s: %v", req.URL, err)
	}
	// Pass the handler a copy whose body replays what was read, including
	// any read error.
	req = req.WithContext(req.Context())
	req.Body = replayBody(body, err)

	ent := Entry{Request: makeRequest(req, body)}
	if addr, ok := req.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		ent.ServerIP = addrIP(addr)
	}

	responseWrapper := &HARResponseWriter{}

	startTime := time.Now()
	c.Handler.ServeHTTP(responseWrapper, req)
	// A handler that never writes a status implies 200 OK.
	responseWrapper.WriteHeader(http.StatusOK)

	// The handler's run time is the only timing visible from the server
	// side, so it is all attributed to wait.
	elapsed := int(time.Since(startTime).Milliseconds())
	ent.Time = elapsed
	ent.Start = startTime.Format(time.RFC3339Nano)
	ent.Timings.Wait = elapsed

	maps.Copy(w.Header(), responseWrapper.sentHeader)
	w.WriteHeader(responseWrapper.statusCode)
	w.Write(responseWrapper.body.Bytes())

	resp := responseWrapper.AsResponse(req)
	ent.Response = makeResponse(resp, responseWrapper.body.Bytes())
	c.addEntry(ent)
}

// HARResponseWriter buffers a handler's response so that it can be recorded
// before being written to the real http.ResponseWriter.
type HARResponseWriter struct {
	body       bytes.Buffer
	statusCode int
	header     http.Header

	// sentHeader is a snapshot of header taken at WriteHeader, since later
	// changes to the header map have no effect on a real response.
	sentHeader      http.Header
	didWriteHeaders bool
}

func (w *HARResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header, 5)
	}
	return w.header
}

func (w *HARResponseWriter) Write(b []byte) (int, error) {
	if !w.didWriteHeaders {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(b)
}

// WriteHeader records the status code. As with a real http.ResponseWriter,
// only the first call takes effect. Informational (1xx) responses are not
// recorded.
func (w *HARResponseWriter) WriteHeader(statusCode int) {
	if w.didWriteHeaders || (statusCode >= 100 && statusCode <= 199) {
		return
	}
	w.statusCode = statusCode
	w.sentHeader = w.Header().Clone()
	w.didWriteHeaders = true
}

func (w *HARResponseWriter) AsResponse(req *http.Request) *http.Response {
	header := w.sentHeader
	if !w.didWriteHeaders {
		header = w.header
	}
	resp := &http.Response{
		StatusCode: w.statusCode,
		Proto:      req.Proto,
		Header:     header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(w.body.Bytes())),
	}
	return resp
}

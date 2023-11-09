package harhar

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"time"
)

// ServeHTTP implements http.Handler (aka a Server-side recorder)
func (c *Recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var err error
	ent := Entry{}
	ent.Request, err = makeRequest(req)
	if err != nil {
		log.Println("unable to record HAR for request ", req.URL.String())
	}

	responseWrapper := &HARResponseWriter{}

	startTime := time.Now()
	c.Handler.ServeHTTP(responseWrapper, req)
	ent.Time = int(time.Since(startTime).Milliseconds())
	ent.Start = startTime.Format(time.RFC3339Nano)
	ent.Timings.Send = -1
	ent.Timings.Receive = -1

	// copy headers
	for h, vals := range responseWrapper.header {
		for _, val := range vals {
			w.Header().Set(h, val)
		}
	}
	w.WriteHeader(responseWrapper.statusCode)
	w.Write(responseWrapper.body.Bytes())

	resp := responseWrapper.AsResponse(req)
	ent.Response, err = makeResponse(resp)
	if err != nil {
		log.Println("unable to record HAR for response ", req.URL.String())
	}
	c.HAR.Log.Entries = append(c.HAR.Log.Entries, ent)
}

type HARResponseWriter struct {
	body       bytes.Buffer
	statusCode int
	header     http.Header

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

func (w *HARResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.didWriteHeaders = true
}

func (w *HARResponseWriter) AsResponse(req *http.Request) *http.Response {
	resp := &http.Response{
		StatusCode: w.statusCode,
		Proto:      req.Proto,
		Header:     w.header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(w.body.Bytes())),
	}
	return resp
}

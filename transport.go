package harhar

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

// Transport returns an http.RoundTripper that records each request made
// through base. If base is nil, http.DefaultTransport is used.
//
// Bodies are recorded as they stream through, so an entry is recorded once
// its response body has been read to the end or closed. As net/http already
// requires, callers must close response bodies.
func (r *Recorder) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{rec: r, base: base}
}

type transport struct {
	rec  *Recorder
	base http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	cfg := &t.rec.cfg
	start := time.Now()
	ent := Entry{StartedDateTime: start, Request: cfg.makeRequest(req)}

	// Send a copy of req so the caller's request is not modified. Its body
	// is recorded as the base transport sends it.
	trace := &requestTrace{}
	out := req.WithContext(httptrace.WithClientTrace(req.Context(), trace.clientTrace()))
	reqBody := newBodyCapture(cfg.maxBodySize)
	if req.Body != nil && req.Body != http.NoBody {
		out.Body = reqBody.wrap(req.Body, nil)
		if req.GetBody != nil {
			out.GetBody = func() (io.ReadCloser, error) {
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				reqBody.reset() // the body is being resent from the start
				return reqBody.wrap(body, nil), nil
			}
		}
	}

	finish := func(gotResponse time.Time) {
		end := time.Now()
		cfg.setPostData(&ent.Request, req.Header.Get("Content-Type"), reqBody)
		ent.Timings, ent.ServerIP = trace.timings(start, gotResponse, end)
		ent.Time = ent.Timings.total()
		t.rec.add(ent)
	}

	resp, err := t.base.RoundTrip(out)
	gotResponse := time.Now()
	if err != nil {
		// Record the failure too, with status 0 as browsers do.
		ent.Response = failedResponse(err)
		finish(gotResponse)
		return nil, err
	}

	// The response knows which protocol was actually used.
	ent.Request.HTTPVersion = resp.Proto
	ent.Response = cfg.makeResponse(resp)
	respBody := newBodyCapture(cfg.maxBodySize)
	recordBody := func(readErr error) {
		ent.Response.Content = cfg.makeContent(resp.Header, respBody)
		ent.Response.BodySize = ent.Response.Content.Size
		if resp.Uncompressed {
			// net/http decompressed the body, so its size on the wire is unknown.
			ent.Response.BodySize = -1
		}
		// Callers often stop reading once they have Content-Length bytes,
		// without waiting for EOF; that's a complete body.
		complete := errors.Is(readErr, errClosedEarly) && resp.ContentLength == ent.Response.Content.Size
		if readErr != nil && !complete {
			ent.Response.Comment = readErr.Error()
		}
		finish(gotResponse)
	}

	if resp.Body == nil || resp.Body == http.NoBody {
		recordBody(nil)
	} else {
		resp.Body = respBody.wrap(resp.Body, recordBody)
	}
	return resp, nil
}

// requestTrace collects connection timestamps for a single request. The
// httptrace hooks may run concurrently, and even after RoundTrip returns, so
// every field is guarded by mu.
type requestTrace struct {
	mu sync.Mutex

	getConn, gotConn          time.Time
	dnsStart, dnsDone         time.Time
	connectStart, connectDone time.Time
	tlsStart, tlsDone         time.Time
	wroteRequest, firstByte   time.Time
	remoteAddr                net.Addr
}

// mark records the current time in *t unless it was already set, so that
// repeated hooks (e.g. parallel dials) keep the earliest time.
func (rt *requestTrace) mark(t *time.Time) {
	rt.mu.Lock()
	if t.IsZero() {
		*t = time.Now()
	}
	rt.mu.Unlock()
}

func (rt *requestTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) { rt.mark(&rt.getConn) },
		GotConn: func(info httptrace.GotConnInfo) {
			rt.mark(&rt.gotConn)
			if info.Conn != nil {
				rt.mu.Lock()
				rt.remoteAddr = info.Conn.RemoteAddr()
				rt.mu.Unlock()
			}
		},
		DNSStart:     func(httptrace.DNSStartInfo) { rt.mark(&rt.dnsStart) },
		DNSDone:      func(httptrace.DNSDoneInfo) { rt.mark(&rt.dnsDone) },
		ConnectStart: func(_, _ string) { rt.mark(&rt.connectStart) },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				rt.mark(&rt.connectDone)
			}
		},
		TLSHandshakeStart: func() { rt.mark(&rt.tlsStart) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				rt.mark(&rt.tlsDone)
			}
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { rt.mark(&rt.wroteRequest) },
		GotFirstResponseByte: func() { rt.mark(&rt.firstByte) },
	}
}

// timings converts the collected timestamps into HAR timings. start is when
// RoundTrip began, gotResponse when the base transport returned, and end
// when the body was finished. It also returns the server IP.
func (rt *requestTrace) timings(start, gotResponse, end time.Time) (Timings, string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	// In HAR, connect includes the TLS handshake.
	connectEnd := rt.connectDone
	if rt.tlsDone.After(connectEnd) {
		connectEnd = rt.tlsDone
	}
	t := Timings{
		Blocked: -1,
		DNS:     msOrNA(rt.dnsStart, rt.dnsDone),
		Connect: msOrNA(rt.connectStart, connectEnd),
		SSL:     msOrNA(rt.tlsStart, rt.tlsDone),
	}
	if !rt.getConn.IsZero() && !rt.gotConn.IsZero() {
		// Blocked is time spent waiting for a connection, excluding setting one up.
		t.Blocked = max(msBetween(rt.getConn, rt.gotConn)-max(t.DNS, 0)-max(t.Connect, 0), 0)
	}
	if rt.gotConn.IsZero() || rt.wroteRequest.IsZero() || rt.firstByte.IsZero() {
		// The base transport didn't report its progress (e.g. it isn't an
		// http.Transport), so count everything up to the response as wait.
		t.Wait = msBetween(start, gotResponse)
		t.Receive = msBetween(gotResponse, end)
	} else {
		t.Send = msBetween(rt.gotConn, rt.wroteRequest)
		t.Wait = msBetween(rt.wroteRequest, rt.firstByte)
		t.Receive = msBetween(rt.firstByte, end)
	}

	var ip string
	if rt.remoteAddr != nil {
		ip = addrIP(rt.remoteAddr)
	}
	return t, ip
}

// total returns the entry time for t: the sum of the timings that apply.
// SSL is already included in Connect.
func (t Timings) total() float64 {
	return roundMs(max(t.Blocked, 0) + max(t.DNS, 0) + max(t.Connect, 0) + t.Send + t.Wait + t.Receive)
}

// addrIP returns the host part of a network address, e.g. "127.0.0.1".
func addrIP(addr net.Addr) string {
	s := addr.String()
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	return s
}

// msBetween returns the milliseconds from start to end, or 0 if either is unset.
func msBetween(start, end time.Time) float64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return roundMs(float64(end.Sub(start)) / float64(time.Millisecond))
}

// msOrNA is like msBetween but returns -1, HAR's "not applicable", if the
// phase didn't happen.
func msOrNA(start, end time.Time) float64 {
	if start.IsZero() || end.IsZero() {
		return -1
	}
	return msBetween(start, end)
}

// roundMs rounds milliseconds to microsecond precision.
func roundMs(ms float64) float64 {
	return float64(int64(ms*1000+0.5)) / 1000
}

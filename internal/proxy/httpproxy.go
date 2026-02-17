package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// hopByHopHeaders are removed from forwarded requests and responses per RFC 7230.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// HTTPProxy handles HTTP CONNECT requests (HTTPS tunneling) and forwards
// plain HTTP requests to the upstream via a pluggable DialContext function.
type HTTPProxy struct {
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	Logger      *slog.Logger

	initOnce     sync.Once
	transportMu  sync.RWMutex
	transport    *http.Transport
	roundTripper http.RoundTripper
}

func (p *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}

	p.handleHTTP(w, r)
}

// Close shuts down the proxy's HTTP transport, releasing idle connections.
func (p *HTTPProxy) Close() {
	p.transportMu.RLock()
	t := p.transport
	p.transportMu.RUnlock()

	if t != nil {
		t.CloseIdleConnections()
	}
}

func (p *HTTPProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	upstream, err := p.DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, fmt.Sprintf("dial upstream: %v", err), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	// hijack the client connection to get a raw net.Conn
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}

	client, buf, err := hj.Hijack()
	if err != nil {
		p.logError("hijack failed", "error", err)
		return
	}
	defer client.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		p.logError("write 200 response failed", "error", err)
		return
	}

	// drain any buffered data the HTTP server already read from the client
	if buffered := buf.Reader.Buffered(); buffered > 0 {
		n, err := io.CopyN(upstream, buf, int64(buffered))
		if err != nil {
			p.logError("draining buffered data failed", "error", err, "expected", buffered, "written", n)
			return
		}
	}

	relay(client, upstream)
}

func (p *HTTPProxy) httpTransport() http.RoundTripper {
	p.initOnce.Do(func() {
		t := &http.Transport{
			DialContext:           p.DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}

		rt := &retryTransport{base: t}

		p.transportMu.Lock()
		p.transport = t
		p.roundTripper = rt
		p.transportMu.Unlock()
	})

	p.transportMu.RLock()
	defer p.transportMu.RUnlock()

	return p.roundTripper
}

func (p *HTTPProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "request URI must be absolute", http.StatusBadRequest)
		return
	}

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	removeHopByHopHeaders(outReq.Header)

	resp, err := p.httpTransport().RoundTrip(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("forwarding request: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	removeHopByHopHeaders(resp.Header)

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}

	w.WriteHeader(resp.StatusCode)

	if _, err := io.Copy(w, resp.Body); err != nil {
		p.logError("copying response body", "error", err)
	}
}

func removeHopByHopHeaders(h http.Header) {
	for _, key := range hopByHopHeaders {
		h.Del(key)
	}
}

// relay copies data bidirectionally between two connections.
// When one direction completes, it closes the destination to unblock the other.
// The caller's defers still call Close, which is safe since net.Conn.Close is idempotent.
func relay(a, b net.Conn) {
	done := make(chan struct{})

	go func() {
		if _, err := io.Copy(b, a); err != nil && !isClosedConnErr(err) {
			logRelayError("relay a→b copy error", err)
		}

		b.Close()
		close(done)
	}()

	if _, err := io.Copy(a, b); err != nil && !isClosedConnErr(err) {
		logRelayError("relay b→a copy error", err)
	}

	a.Close()
	<-done
}

func isClosedConnErr(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF)
}

// logRelayError logs relay errors, promoting broken pipe errors to Warn
// since they indicate a connection dropped by the remote side.
func logRelayError(msg string, err error) {
	if isBrokenPipeErr(err) {
		slog.Warn(msg, "error", err)
		return
	}

	slog.Debug(msg, "error", err)
}

func (p *HTTPProxy) logError(msg string, args ...any) {
	if p.Logger != nil {
		p.Logger.Error(msg, args...)
	}
}

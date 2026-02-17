package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"syscall"
)

// roundTripCloser combines RoundTrip with the ability to close idle connections.
// Both *http.Transport and test mocks satisfy this interface.
type roundTripCloser interface {
	http.RoundTripper
	CloseIdleConnections()
}

// retryTransport wraps a transport and retries once on broken pipe or connection
// reset errors. This handles the case where the transport's connection pool
// contains a stale connection whose underlying SPDY stream was closed server-side.
type retryTransport struct {
	base roundTripCloser
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// buffer the body so it can be replayed on retry
	var bodyBytes []byte

	if req.Body != nil {
		var err error

		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}

		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	resp, err := t.base.RoundTrip(req)
	if err == nil || !isBrokenPipeErr(err) {
		return resp, err
	}

	// evict stale connections and retry with a fresh one
	t.base.CloseIdleConnections()

	if bodyBytes != nil {
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	return t.base.RoundTrip(req)
}

// isBrokenPipeErr returns true if the error indicates a broken pipe or
// connection reset, which typically means a stale pooled connection.
func isBrokenPipeErr(err error) bool {
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	// fallback string matching for wrapped errors that lose the syscall type
	msg := err.Error()

	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset")
}

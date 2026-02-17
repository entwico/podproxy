package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
)

// mockRoundTripCloser implements roundTripCloser for testing.
type mockRoundTripCloser struct {
	responses   []*http.Response
	errors      []error
	calls       int
	bodies      []string
	idlesClosed int
}

func (m *mockRoundTripCloser) RoundTrip(req *http.Request) (*http.Response, error) {
	idx := m.calls
	m.calls++

	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		m.bodies = append(m.bodies, string(body))
	} else {
		m.bodies = append(m.bodies, "")
	}

	if idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}

	if idx < len(m.responses) {
		return m.responses[idx], nil
	}

	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func (m *mockRoundTripCloser) CloseIdleConnections() {
	m.idlesClosed++
}

func TestRetryTransport_PassesThroughOnSuccess(t *testing.T) {
	mock := &mockRoundTripCloser{
		responses: []*http.Response{
			{StatusCode: http.StatusOK, Body: http.NoBody},
		},
	}

	rt := &retryTransport{base: mock}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if mock.calls != 1 {
		t.Errorf("calls = %d, want 1", mock.calls)
	}

	if mock.idlesClosed != 0 {
		t.Errorf("idlesClosed = %d, want 0", mock.idlesClosed)
	}
}

func TestRetryTransport_RetriesOnBrokenPipe(t *testing.T) {
	mock := &mockRoundTripCloser{
		errors: []error{syscall.EPIPE, nil},
		responses: []*http.Response{
			nil,
			{StatusCode: http.StatusOK, Body: http.NoBody},
		},
	}

	rt := &retryTransport{base: mock}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if mock.calls != 2 {
		t.Errorf("calls = %d, want 2", mock.calls)
	}

	if mock.idlesClosed != 1 {
		t.Errorf("idlesClosed = %d, want 1", mock.idlesClosed)
	}
}

func TestRetryTransport_RetriesOnConnectionReset(t *testing.T) {
	mock := &mockRoundTripCloser{
		errors: []error{syscall.ECONNRESET, nil},
		responses: []*http.Response{
			nil,
			{StatusCode: http.StatusOK, Body: http.NoBody},
		},
	}

	rt := &retryTransport{base: mock}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if mock.calls != 2 {
		t.Errorf("calls = %d, want 2", mock.calls)
	}
}

func TestRetryTransport_NoRetryOnOtherErrors(t *testing.T) {
	mock := &mockRoundTripCloser{
		errors: []error{errors.New("some other error")},
	}

	rt := &retryTransport{base: mock}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	resp, err := rt.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error")
	}

	if mock.calls != 1 {
		t.Errorf("calls = %d, want 1 (should not retry non-broken-pipe errors)", mock.calls)
	}

	if mock.idlesClosed != 0 {
		t.Errorf("idlesClosed = %d, want 0", mock.idlesClosed)
	}
}

func TestRetryTransport_PreservesRequestBody(t *testing.T) {
	const body = "request payload"

	mock := &mockRoundTripCloser{
		errors: []error{syscall.EPIPE, nil},
		responses: []*http.Response{
			nil,
			{StatusCode: http.StatusOK, Body: http.NoBody},
		},
	}

	rt := &retryTransport{base: mock}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader(body))

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if len(mock.bodies) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(mock.bodies))
	}

	if mock.bodies[0] != body {
		t.Errorf("first call body = %q, want %q", mock.bodies[0], body)
	}

	if mock.bodies[1] != body {
		t.Errorf("retry call body = %q, want %q", mock.bodies[1], body)
	}
}

package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"
	"time"
)

func TestClusterSuffix(t *testing.T) {
	dialer := &ClusterDialer{
		Forwarders: map[string]*PortForwarder{
			"production": {},
			"staging":    {},
		},
	}

	tests := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "known cluster two parts",
			addr: "redis.production:6379",
			want: "production",
		},
		{
			name: "known cluster three parts",
			addr: "redis.default.staging:6379",
			want: "staging",
		},
		{
			name: "known cluster with svc suffix",
			addr: "redis.production.svc:6379",
			want: "production",
		},
		{
			name: "known cluster with svc.cluster.local suffix",
			addr: "redis.default.production.svc.cluster.local:6379",
			want: "production",
		},
		{
			name: "unknown cluster suffix",
			addr: "redis.unknown:6379",
			want: "",
		},
		{
			name: "plain hostname passthrough",
			addr: "example.com:443",
			want: "",
		},
		{
			name: "multi-segment hostname passthrough",
			addr: "api.github.com:443",
			want: "",
		},
		{
			name: "single-part hostname passthrough",
			addr: "localhost:8080",
			want: "",
		},
		{
			name: "missing port",
			addr: "redis.production",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dialer.clusterSuffix(tt.addr)
			if got != tt.want {
				t.Errorf("clusterSuffix(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

// direct pod target used by dial retry tests (no service resolution).
var directPodTarget = Target{
	PodName:   "mypod",
	Namespace: "ns",
	Port:      8080,
}

// service target used by service resolution retry tests.
var serviceTarget = Target{
	IsService:   true,
	ServiceName: "mysvc",
	Namespace:   "ns",
	Port:        8080,
}

func TestDialTarget_Success(t *testing.T) {
	fwd := &PortForwarder{
		dialFunc: func(_, _ string, _ int) (*StreamConn, error) {
			return &StreamConn{errDone: make(chan struct{})}, nil
		},
	}

	conn, err := fwd.dialTarget(context.Background(), "mypod.ns.cluster:8080", directPodTarget)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if conn == nil {
		t.Fatal("expected non-nil connection")
	}
}

func TestDialTarget_RetriesOnTransientDialError(t *testing.T) {
	var attempts int

	fwd := &PortForwarder{
		baseBackoff: time.Millisecond,
		dialFunc: func(_, _ string, _ int) (*StreamConn, error) {
			attempts++
			if attempts < 3 {
				return nil, fmt.Errorf("SPDY dial: %w", syscall.ECONNRESET)
			}

			return &StreamConn{errDone: make(chan struct{})}, nil
		},
	}

	conn, err := fwd.dialTarget(context.Background(), "mypod.ns.cluster:8080", directPodTarget)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if conn == nil {
		t.Fatal("expected non-nil connection")
	}

	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestDialTarget_ExhaustsRetries(t *testing.T) {
	var attempts int

	fwd := &PortForwarder{
		baseBackoff: time.Millisecond,
		dialFunc: func(_, _ string, _ int) (*StreamConn, error) {
			attempts++
			return nil, fmt.Errorf("dial: %w", io.EOF)
		},
	}

	_, err := fwd.dialTarget(context.Background(), "mypod.ns.cluster:8080", directPodTarget)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}

	if attempts != dialMaxAttempts {
		t.Errorf("attempts = %d, want %d", attempts, dialMaxAttempts)
	}
}

func TestDialTarget_NoRetryOnNonTransientError(t *testing.T) {
	var attempts int

	fwd := &PortForwarder{
		dialFunc: func(_, _ string, _ int) (*StreamConn, error) {
			attempts++
			return nil, errors.New("permission denied")
		},
	}

	_, err := fwd.dialTarget(context.Background(), "mypod.ns.cluster:8080", directPodTarget)
	if err == nil {
		t.Fatal("expected error")
	}

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (should not retry non-transient errors)", attempts)
	}
}

func TestDialTarget_RespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var attempts int

	fwd := &PortForwarder{
		baseBackoff: time.Millisecond,
		dialFunc: func(_, _ string, _ int) (*StreamConn, error) {
			attempts++
			// cancel context after first attempt so the retry loop exits
			cancel()

			return nil, fmt.Errorf("dial: %w", syscall.EPIPE)
		},
	}

	_, err := fwd.dialTarget(ctx, "mypod.ns.cluster:8080", directPodTarget)
	if err == nil {
		t.Fatal("expected error")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

func TestDialTarget_ReResolvesServiceOnRetry(t *testing.T) {
	var resolveAttempts, dialAttempts int

	fwd := &PortForwarder{
		baseBackoff: time.Millisecond,
		resolveFunc: func(_ context.Context, _, _ string) (string, error) {
			resolveAttempts++
			return fmt.Sprintf("pod-%d", resolveAttempts), nil
		},
		dialFunc: func(_, pod string, _ int) (*StreamConn, error) {
			dialAttempts++
			// fail the first pod, succeed on the second
			if pod == "pod-1" {
				return nil, fmt.Errorf("dial: %w", syscall.ECONNREFUSED)
			}

			return &StreamConn{errDone: make(chan struct{})}, nil
		},
	}

	conn, err := fwd.dialTarget(context.Background(), "mysvc.ns.cluster:8080", serviceTarget)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if conn == nil {
		t.Fatal("expected non-nil connection")
	}

	if resolveAttempts != 2 {
		t.Errorf("resolveAttempts = %d, want 2 (should re-resolve on retry)", resolveAttempts)
	}

	if dialAttempts != 2 {
		t.Errorf("dialAttempts = %d, want 2", dialAttempts)
	}
}

func TestDialTarget_RetriesOnNoReadyPodEndpoints(t *testing.T) {
	var resolveAttempts int

	fwd := &PortForwarder{
		baseBackoff: time.Millisecond,
		resolveFunc: func(_ context.Context, _, _ string) (string, error) {
			resolveAttempts++
			if resolveAttempts < 3 {
				return "", errors.New("no ready pod endpoints found for service ns/mysvc")
			}

			return "ready-pod", nil
		},
		dialFunc: func(_, _ string, _ int) (*StreamConn, error) {
			return &StreamConn{errDone: make(chan struct{})}, nil
		},
	}

	conn, err := fwd.dialTarget(context.Background(), "mysvc.ns.cluster:8080", serviceTarget)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if conn == nil {
		t.Fatal("expected non-nil connection")
	}

	if resolveAttempts != 3 {
		t.Errorf("resolveAttempts = %d, want 3", resolveAttempts)
	}
}

func TestDialTarget_NoRetryOnNonTransientResolveError(t *testing.T) {
	var resolveAttempts int

	fwd := &PortForwarder{
		resolveFunc: func(_ context.Context, _, _ string) (string, error) {
			resolveAttempts++
			return "", errors.New("forbidden")
		},
		dialFunc: func(_, _ string, _ int) (*StreamConn, error) {
			t.Fatal("dialFunc should not be called when resolve fails with non-transient error")
			return nil, nil
		},
	}

	_, err := fwd.dialTarget(context.Background(), "mysvc.ns.cluster:8080", serviceTarget)
	if err == nil {
		t.Fatal("expected error")
	}

	if resolveAttempts != 1 {
		t.Errorf("resolveAttempts = %d, want 1", resolveAttempts)
	}
}

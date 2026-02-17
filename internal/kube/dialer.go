package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport/spdy"
)

// ClusterDialer routes connections to the correct cluster's KubePortForwarder
// based on the cluster name extracted from the DNS address.
type ClusterDialer struct {
	Forwarders map[string]*PortForwarder
}

// DialContext routes the connection based on the destination address. If the
// address matches a known cluster name, it dials via Kubernetes port-forwarding.
// Otherwise it falls through to a direct TCP connection (passthrough).
func (d *ClusterDialer) DialContext(ctx context.Context, network string, addr string) (net.Conn, error) {
	if cluster := d.clusterSuffix(addr); cluster != "" {
		target, err := ParseTarget(addr)
		if err != nil {
			return nil, err
		}

		fwd := d.Forwarders[cluster]
		if fwd == nil {
			return nil, fmt.Errorf("cluster %q not found in forwarders map", cluster)
		}

		// fill in cluster's default namespace when not specified in the address.
		if target.Namespace == "" {
			target.Namespace = fwd.DefaultNamespace
		}

		return fwd.dialTarget(ctx, addr, target)
	}

	// passthrough: address does not match any known cluster, dial directly.
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// clusterSuffix extracts the cluster name from addr if it matches a known
// cluster in the Forwarders map. Returns empty string for non-Kubernetes addresses.
func (d *ClusterDialer) clusterSuffix(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}

	host = strings.TrimSuffix(host, ".svc.cluster.local")
	host = strings.TrimSuffix(host, ".svc")

	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}

	candidate := parts[len(parts)-1]
	if _, ok := d.Forwarders[candidate]; ok {
		return candidate
	}

	return ""
}

// ensure ClusterDialer.DialContext matches the expected signature.
var _ func(context.Context, string, string) (net.Conn, error) = (*ClusterDialer)(nil).DialContext

// PortForwarder dials Kubernetes pods via SPDY port-forwarding.
type PortForwarder struct {
	Config           *rest.Config
	Clientset        *kubernetes.Clientset
	DefaultNamespace string
	Logger           *slog.Logger

	// test overrides — if nil/zero, the real implementations and defaults are used.
	dialFunc    func(namespace, pod string, port int) (*StreamConn, error)
	resolveFunc func(ctx context.Context, namespace, serviceName string) (string, error)
	baseBackoff time.Duration
}

const (
	dialMaxAttempts  = 6
	dialBaseBackoff  = 1 * time.Second
	dialBackoffScale = 2
)

// dialTarget resolves the pre-parsed target and dials the pod with retries.
// For service targets, each retry re-resolves the service to pick a different
// ready pod (e.g. after a rolling restart). This gives the retry loop a ~31s
// window (1s + 2s + 4s + 8s + 16s) which covers most pod restart scenarios.
func (k *PortForwarder) dialTarget(ctx context.Context, originalAddr string, target Target) (net.Conn, error) {
	dial := k.dialFunc
	if dial == nil {
		dial = k.dialPod
	}

	resolve := k.resolveFunc
	if resolve == nil {
		resolve = func(ctx context.Context, ns, svc string) (string, error) {
			return ResolveServiceToPod(ctx, k.Clientset, ns, svc)
		}
	}

	var lastErr error

	for attempt := range dialMaxAttempts {
		podName := target.PodName

		if target.IsService {
			var err error

			podName, err = resolve(ctx, target.Namespace, target.ServiceName)
			if err != nil {
				lastErr = err

				if !isRetriableError(err) {
					break
				}

				if ok := k.waitBackoff(ctx, attempt, target.Namespace, target.ServiceName, 0, err); !ok {
					return nil, fmt.Errorf("dial retry cancelled: %w", ctx.Err())
				}

				continue
			}

			if attempt == 0 && k.Logger != nil {
				k.Logger.Info("resolved service to pod", "namespace", target.Namespace, "service", target.ServiceName, "pod", podName)
			}
		}

		conn, err := dial(target.Namespace, podName, target.Port)
		if err == nil {
			resolvedTarget := fmt.Sprintf("%s/%s:%d", target.Namespace, podName, target.Port)

			if k.Logger != nil {
				k.Logger.Info("connect", "addr", originalAddr, "target", resolvedTarget)
			}

			return &logOnCloseConn{
				StreamConn: conn,
				logger:     k.Logger,
				origAddr:   originalAddr,
				resolved:   resolvedTarget,
			}, nil
		}

		lastErr = err

		if !isRetriableError(err) {
			break
		}

		if ok := k.waitBackoff(ctx, attempt, target.Namespace, podName, target.Port, err); !ok {
			return nil, fmt.Errorf("dial retry cancelled: %w", ctx.Err())
		}
	}

	if k.Logger != nil {
		k.Logger.Error("failed to connect", "addr", originalAddr, "error", lastErr)
	}

	return nil, lastErr
}

// waitBackoff sleeps for the exponential backoff duration, logging the retry.
// Returns false if the context was cancelled during the wait.
func (k *PortForwarder) waitBackoff(ctx context.Context, attempt int, namespace, name string, port int, err error) bool {
	// don't sleep after the last attempt
	if attempt == dialMaxAttempts-1 {
		return true
	}

	base := k.baseBackoff
	if base == 0 {
		base = dialBaseBackoff
	}

	backoff := base * time.Duration(pow(dialBackoffScale, attempt))

	if k.Logger != nil {
		k.Logger.Warn("retrying connection",
			"namespace", namespace, "target", name, "port", port,
			"attempt", attempt+1, "backoff", backoff, "error", err,
		)
	}

	select {
	case <-ctx.Done():
		return false
	case <-time.After(backoff):
		return true
	}
}

func pow(base, exp int) int {
	result := 1
	for range exp {
		result *= base
	}

	return result
}

// isRetriableError returns true for transient errors that are safe to retry.
// This includes network errors (broken pipe, connection reset, refused, EOF,
// timeouts) and service resolution failures (no ready pods during a restart).
func isRetriableError(err error) bool {
	if errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// "no ready pod endpoints" happens when a service's pods are restarting
	if strings.Contains(err.Error(), "no ready pod endpoints") {
		return true
	}

	return false
}

// dialPod establishes an SPDY port-forward connection to the given pod and port.
func (k *PortForwarder) dialPod(namespace, pod string, port int) (*StreamConn, error) {
	reqURL := k.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("portforward").
		URL()

	// create the SPDY transport using the rest config (handles auth, TLS, etc).
	transport, upgrader, err := spdy.RoundTripperFor(k.Config)
	if err != nil {
		return nil, fmt.Errorf("creating SPDY round tripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, reqURL)

	spdyConn, protocol, err := dialer.Dial(portForwardProtocolV1)
	if err != nil {
		return nil, fmt.Errorf("SPDY dial to %s/%s: %w", namespace, pod, err)
	}

	_ = protocol // expected to be "portforward.k8s.io"

	// both streams share the same requestID and port.
	requestID := "0"
	headers := http.Header{}
	headers.Set("Streamtype", "error")
	headers.Set("Port", strconv.Itoa(port))
	headers.Set("Requestid", requestID)

	// error stream must be created first (Kubernetes protocol requirement).
	errorStream, err := spdyConn.CreateStream(headers)
	if err != nil {
		spdyConn.Close()
		return nil, fmt.Errorf("creating error stream: %w", err)
	}

	headers.Set("Streamtype", "data")

	dataStream, err := spdyConn.CreateStream(headers)
	if err != nil {
		errorStream.Close()
		spdyConn.Close()

		return nil, fmt.Errorf("creating data stream: %w", err)
	}

	target := fmt.Sprintf("%s/%s:%d", namespace, pod, port)

	return NewStreamConn(dataStream, errorStream, spdyConn, target), nil
}

const portForwardProtocolV1 = "portforward.k8s.io"

// logOnCloseConn wraps a StreamConn and logs connection metrics on close.
type logOnCloseConn struct {
	*StreamConn

	logger   *slog.Logger
	origAddr string
	resolved string
}

func (c *logOnCloseConn) Close() error {
	err := c.StreamConn.Close()

	if c.logger != nil {
		c.logger.Info("closed",
			"addr", c.origAddr,
			"target", c.resolved,
			"duration", c.Duration().Round(100*time.Millisecond).String(),
			"rx", formatBytes(c.BytesRead()),
			"tx", formatBytes(c.BytesWritten()),
		)
	}

	return err
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

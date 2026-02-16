package kube

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
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
}

// dialTarget resolves the pre-parsed target and establishes an SPDY port-forward.
func (k *PortForwarder) dialTarget(ctx context.Context, originalAddr string, target Target) (net.Conn, error) {
	podName := target.PodName

	if target.IsService {
		var err error

		podName, err = ResolveServiceToPod(ctx, k.Clientset, target.Namespace, target.ServiceName)
		if err != nil {
			return nil, err
		}

		if k.Logger != nil {
			k.Logger.Info("resolved service to pod", "namespace", target.Namespace, "service", target.ServiceName, "pod", podName)
		}
	}

	conn, err := k.dialPod(target.Namespace, podName, target.Port)
	if err != nil {
		if k.Logger != nil {
			k.Logger.Error("failed to dial pod", "namespace", target.Namespace, "pod", podName, "port", target.Port, "error", err)
		}

		return nil, err
	}

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

package kube

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Resolver is a no-op DNS resolver that always succeeds.
// The go-socks5 library resolves hostnames via system DNS by default, which
// fails for Kubernetes service names. This resolver skips DNS so the FQDN
// is passed through to our DialContext where we handle Kubernetes resolution.
type Resolver struct{}

func (r Resolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

// Target represents a resolved Kubernetes destination for port-forwarding.
type Target struct {
	Cluster     string
	IsService   bool
	ServiceName string
	PodName     string
	Namespace   string
	Port        int
}

// ParseTarget parses a SOCKS5 destination address into a Kubernetes Target.
// The last dot-separated segment of the hostname identifies the cluster.
//
// Supported formats (after stripping .svc.cluster.local / .svc suffixes):
//
//	<svc>.<cluster>:<port>                → service in cluster's default namespace
//	<svc>.<ns>.<cluster>:<port>           → service in namespace <ns>
//	<pod>.<svc>.<ns>.<cluster>:<port>     → direct pod (StatefulSet pattern)
func ParseTarget(addr string) (Target, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return Target{}, fmt.Errorf("invalid address %q: %w", addr, err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return Target{}, fmt.Errorf("invalid port %q: %w", portStr, err)
	}

	if port < 1 || port > 65535 {
		return Target{}, fmt.Errorf("port %d out of range 1-65535", port)
	}

	// strip common Kubernetes DNS suffixes.
	host = strings.TrimSuffix(host, ".svc.cluster.local")
	host = strings.TrimSuffix(host, ".svc")

	parts := strings.Split(host, ".")

	switch len(parts) {
	case 2:
		// <svc>.<cluster>:<port>
		return Target{
			Cluster:     parts[1],
			IsService:   true,
			ServiceName: parts[0],
			Port:        port,
		}, nil
	case 3:
		// <svc>.<ns>.<cluster>:<port>
		return Target{
			Cluster:     parts[2],
			IsService:   true,
			ServiceName: parts[0],
			Namespace:   parts[1],
			Port:        port,
		}, nil
	case 4:
		// <pod>.<svc>.<ns>.<cluster>:<port>
		return Target{
			Cluster:     parts[3],
			IsService:   false,
			PodName:     parts[0],
			ServiceName: parts[1],
			Namespace:   parts[2],
			Port:        port,
		}, nil
	default:
		return Target{}, fmt.Errorf("unsupported address format %q: expected 2-4 dot-separated components", host)
	}
}

// Package podproxy routes matched TCP connections through the podproxy SOCKS5
// proxy during local development.
//
// Activation is controlled by the GO_PODPROXY environment variable; when it is
// unset every helper degrades to a plain net.Dialer passthrough, so the package
// is safe to wire in unconditionally and ship in production binaries.
//
// Environment variables:
//   - GO_PODPROXY: "true"/"1" enables proxying via the default podproxy SOCKS5
//     address (socks5://127.0.0.1:9080); a socks5:// URL enables it with a
//     custom address; unset/empty disables everything
//   - GO_PODPROXY_PAC_URL: PAC endpoint to load host patterns from
//     (default http://127.0.0.1:9082)
//   - GO_PODPROXY_MATCH: additional host regexp to proxy
//   - GO_PODPROXY_LOG: error (default), info, debug
package podproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/net/proxy"
)

const (
	defaultSocksProxy = "socks5://127.0.0.1:9080"
	defaultPacURL     = "http://127.0.0.1:9082"
)

type state struct {
	enabled  bool
	log      *logger
	direct   net.Dialer
	socks    proxy.ContextDialer
	patterns []*regexp.Regexp
}

var getState = sync.OnceValue(newStateFromEnv)

func newStateFromEnv() *state {
	value := os.Getenv("GO_PODPROXY")

	if value == "" {
		return &state{enabled: false, log: &logger{level: levelError}}
	}

	rawLevel := os.Getenv("GO_PODPROXY_LOG")

	level, ok := parseLogLevel(rawLevel)
	if !ok {
		(&logger{}).errorf("invalid GO_PODPROXY_LOG=%q, expected: error, info, debug", rawLevel)
		os.Exit(1)
	}

	log := &logger{level: level}

	var socksURL string

	switch {
	case value == "true" || value == "1":
		socksURL = defaultSocksProxy
	case strings.HasPrefix(value, "socks5://"):
		socksURL = value
	default:
		log.errorf(`invalid GO_PODPROXY=%q, expected "true" or a socks5:// URL`, value)
		os.Exit(1)
	}

	parsed, err := url.Parse(socksURL)
	if err != nil || parsed.Host == "" {
		log.errorf("invalid GO_PODPROXY proxy URL %q", socksURL)
		os.Exit(1)
	}

	pacURL := os.Getenv("GO_PODPROXY_PAC_URL")
	if pacURL == "" {
		pacURL = defaultPacURL
	}

	patterns := loadPatterns(os.Getenv("GO_PODPROXY_MATCH"), pacURL, log)

	s, err := newState(parsed.Host, patterns, log)
	if err != nil {
		log.errorf("failed to create SOCKS5 dialer for %s: %v", parsed.Host, err)
		os.Exit(1)
	}

	log.infof("SOCKS5 proxy enabled: %s (%d patterns)", parsed.Host, len(patterns))

	return s
}

func newState(socksHostPort string, patterns []*regexp.Regexp, log *logger) (*state, error) {
	s := &state{enabled: true, log: log, patterns: patterns}

	dialer, err := proxy.SOCKS5("tcp", socksHostPort, nil, &s.direct)
	if err != nil {
		return nil, err
	}

	socks, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS5 dialer does not implement DialContext")
	}

	s.socks = socks

	return s, nil
}

func (s *state) shouldProxy(host string) bool {
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
		return false
	}

	for _, pattern := range s.patterns {
		if pattern.MatchString(host) {
			return true
		}
	}

	return false
}

func (s *state) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if s.enabled && strings.HasPrefix(network, "tcp") {
		if host, _, err := net.SplitHostPort(addr); err == nil && s.shouldProxy(host) {
			s.log.infof("%s", addr)

			return s.socks.DialContext(ctx, "tcp", addr)
		}
	}

	return s.direct.DialContext(ctx, network, addr)
}

// Install patches http.DefaultTransport so all default HTTP/HTTPS traffic is
// routed through podproxy for matched hosts. It is a no-op unless GO_PODPROXY
// is set. Clients constructed with their own transport are not affected — pass
// [DialContext] to them explicitly.
func Install() {
	s := getState()

	if !s.enabled {
		return
	}

	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport.DialContext = DialContext
	}
}

// DialContext dials addr, routing it through podproxy when the host matches a
// proxied pattern and proxying is enabled; otherwise it behaves exactly like
// net.Dialer. It has the de-facto standard dialer signature accepted by
// http.Transport.DialContext, redis Options.Dialer, pgconn Config.DialFunc, etc.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return getState().dialContext(ctx, network, addr)
}

// ContextDialer adapts [DialContext] to the single-method dialer interface used
// by clients like the mongo driver (options.Client().SetDialer).
type ContextDialer struct{}

// DialContext implements the dialer interface by delegating to [DialContext].
func (ContextDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return DialContext(ctx, network, addr)
}

// Dialer returns a [ContextDialer] for clients that take a dialer interface.
func Dialer() ContextDialer {
	return ContextDialer{}
}

// GrpcDialer adapts [DialContext] to the signature of grpc.WithContextDialer.
// Use it together with a passthrough:/// target so gRPC hands the unresolved
// hostname to the dialer instead of resolving it client-side.
func GrpcDialer() func(ctx context.Context, addr string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		return DialContext(ctx, "tcp", addr)
	}
}

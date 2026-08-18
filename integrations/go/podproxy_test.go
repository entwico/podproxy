package podproxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"regexp"
	"sync"
	"testing"
	"time"
)

// minimal SOCKS5 server that records the requested target and pipes the
// connection to 127.0.0.1 at the requested port.
type testSocksServer struct {
	listener net.Listener

	mu      sync.Mutex
	targets []string
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()

	listener, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { listener.Close() })

	return listener
}

func startTestSocksServer(t *testing.T) *testSocksServer {
	t.Helper()

	listener := listenLoopback(t)

	server := &testSocksServer{listener: listener}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go server.handle(conn)
		}
	}()

	return server
}

func (s *testSocksServer) addr() string {
	return s.listener.Addr().String()
}

func (s *testSocksServer) requestedTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.targets...)
}

func (s *testSocksServer) handle(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)

	// greeting: VER NMETHODS METHODS...
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 5 {
		return
	}

	methods := make([]byte, header[1])
	if _, err := io.ReadFull(reader, methods); err != nil {
		return
	}

	_, _ = conn.Write([]byte{5, 0})

	// request: VER CMD RSV ATYP
	request := make([]byte, 4)
	if _, err := io.ReadFull(reader, request); err != nil || request[1] != 1 || request[3] != 3 {
		return
	}

	hostLen, err := reader.ReadByte()
	if err != nil {
		return
	}

	host := make([]byte, hostLen)
	if _, err := io.ReadFull(reader, host); err != nil {
		return
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return
	}

	port := binary.BigEndian.Uint16(portBytes)

	s.mu.Lock()
	s.targets = append(s.targets, fmt.Sprintf("%s:%d", host, port))
	s.mu.Unlock()

	backend, err := new(net.Dialer).DialContext(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})

		return
	}

	defer backend.Close()

	_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})

	done := make(chan struct{})

	go func() {
		_, _ = io.Copy(backend, reader)

		close(done)
	}()

	_, _ = io.Copy(conn, backend)

	<-done
}

func startEchoServer(t *testing.T) net.Listener {
	t.Helper()

	listener := listenLoopback(t)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() {
				defer conn.Close()

				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	return listener
}

func echoRoundTrip(t *testing.T, conn net.Conn) {
	t.Helper()

	message := "hello through podproxy"

	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	response := make([]byte, len(message))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}

	if string(response) != message {
		t.Fatalf("expected %q, got %q", message, response)
	}
}

func TestDialContextProxiesMatchedHost(t *testing.T) {
	socks := startTestSocksServer(t)
	echo := startEchoServer(t)

	s, err := newState(socks.addr(), []*regexp.Regexp{regexp.MustCompile(`\.test-cluster$`)}, &logger{})
	if err != nil {
		t.Fatal(err)
	}

	echoPort := echo.Addr().(*net.TCPAddr).Port
	target := fmt.Sprintf("redis.cache.test-cluster:%d", echoPort)

	conn, err := s.dialContext(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	echoRoundTrip(t, conn)

	targets := socks.requestedTargets()

	if len(targets) != 1 || targets[0] != target {
		t.Fatalf("expected SOCKS target %q, got %v", target, targets)
	}
}

func TestDialContextUnmatchedHostGoesDirect(t *testing.T) {
	socks := startTestSocksServer(t)
	echo := startEchoServer(t)

	s, err := newState(socks.addr(), []*regexp.Regexp{regexp.MustCompile(`\.test-cluster$`)}, &logger{})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := s.dialContext(context.Background(), "tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	echoRoundTrip(t, conn)

	if targets := socks.requestedTargets(); len(targets) != 0 {
		t.Fatalf("expected no SOCKS targets, got %v", targets)
	}
}

func TestDialContextDisabledIsPassthrough(t *testing.T) {
	echo := startEchoServer(t)

	s := &state{enabled: false, log: &logger{}}

	conn, err := s.dialContext(context.Background(), "tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	echoRoundTrip(t, conn)
}

func TestShouldProxyBypassesLocalhost(t *testing.T) {
	s := &state{enabled: true, patterns: []*regexp.Regexp{regexp.MustCompile(`.*`)}}

	for _, host := range []string{"", "localhost", "127.0.0.1", "::1"} {
		if s.shouldProxy(host) {
			t.Errorf("host %q should be bypassed", host)
		}
	}

	if !s.shouldProxy("redis.staging") {
		t.Error("redis.staging should be proxied")
	}
}

func TestNewStateFromEnvDisabled(t *testing.T) {
	t.Setenv("GO_PODPROXY", "")

	s := newStateFromEnv()

	if s.enabled {
		t.Fatal("expected disabled state when GO_PODPROXY is unset")
	}
}

func TestNewStateFromEnvEnabled(t *testing.T) {
	t.Setenv("GO_PODPROXY", "true")
	t.Setenv("GO_PODPROXY_PAC_URL", "http://127.0.0.1:1")
	t.Setenv("GO_PODPROXY_MATCH", `\.env-cluster$`)

	s := newStateFromEnv()

	if !s.enabled {
		t.Fatal("expected enabled state")
	}

	if !s.shouldProxy("redis.env-cluster") {
		t.Error("GO_PODPROXY_MATCH pattern should apply")
	}

	if s.shouldProxy("example.com") {
		t.Error("unmatched host should not be proxied")
	}
}

func TestNewStateFromEnvCustomProxyURL(t *testing.T) {
	socks := startTestSocksServer(t)
	echo := startEchoServer(t)

	t.Setenv("GO_PODPROXY", "socks5://"+socks.addr())
	t.Setenv("GO_PODPROXY_PAC_URL", "http://127.0.0.1:1")
	t.Setenv("GO_PODPROXY_MATCH", `\.custom-cluster$`)

	s := newStateFromEnv()

	echoPort := echo.Addr().(*net.TCPAddr).Port

	conn, err := s.dialContext(context.Background(), "tcp", fmt.Sprintf("svc.custom-cluster:%d", echoPort))
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()

	echoRoundTrip(t, conn)
}

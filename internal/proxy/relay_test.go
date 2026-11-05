package proxy

import (
	"io"
	"net"
	"testing"
	"time"
)

// tcpPair returns the two ends of a loopback TCP connection.
func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer l.Close()

	accepted := make(chan net.Conn, 1)

	go func() {
		conn, _ := l.Accept()
		accepted <- conn
	}()

	dialed, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}

	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}

	t.Cleanup(func() {
		dialed.Close()
		server.Close()
	})

	dialedTCP, ok := dialed.(*net.TCPConn)
	if !ok {
		t.Fatalf("dialed %T, want *net.TCPConn", dialed)
	}

	serverTCP, ok := server.(*net.TCPConn)
	if !ok {
		t.Fatalf("accepted %T, want *net.TCPConn", server)
	}

	return dialedTCP, serverTCP
}

func TestRelayKeepsTheReplyAfterAHalfClose(t *testing.T) {
	client, proxyClientSide := tcpPair(t)
	proxyUpstreamSide, upstream := tcpPair(t)

	go relay(proxyClientSide, proxyUpstreamSide)

	// the upstream answers only after it has read the whole request
	go func() {
		request, _ := io.ReadAll(upstream)
		_, _ = upstream.Write([]byte("response to " + string(request)))
		upstream.Close()
	}()

	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := client.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}

	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	reply, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(reply) != "response to request" {
		t.Errorf("reply = %q, want the response sent after the client's EOF", reply)
	}
}

func TestRelayEndsWhenTheDestinationCannotHalfClose(t *testing.T) {
	client, proxyClientSide := tcpPair(t)
	// net.Pipe has no CloseWrite, so the client's EOF closes it fully
	proxyUpstreamSide, upstream := net.Pipe()

	go func() { _, _ = io.Copy(io.Discard, upstream) }()

	done := make(chan struct{})

	go func() {
		relay(proxyClientSide, proxyUpstreamSide)
		close(done)
	}()

	if err := client.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not end after the client's EOF")
	}
}

package proxy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func newTestSigner(t *testing.T) ssh.Signer {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}

	return signer
}

// startSSHProxy serves an SSHProxy on a loopback port that authorizes clientKey
// and returns its address and host key.
func startSSHProxy(t *testing.T, clientKey ssh.Signer, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (string, ssh.PublicKey) {
	t.Helper()

	hostKey := newTestSigner(t)
	p := &SSHProxy{
		DialContext: dial,
		Config:      NewSSHServerConfig(hostKey, []ssh.PublicKey{clientKey.PublicKey()}),
	}

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	go func() { _ = p.Serve(l) }()

	t.Cleanup(func() { p.Close() })

	return l.Addr().String(), hostKey.PublicKey()
}

func dialSSH(t *testing.T, addr string, hostKey ssh.PublicKey, key ssh.Signer) (*ssh.Client, error) {
	t.Helper()

	return ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "anyone",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         5 * time.Second,
	})
}

func TestSSHProxyForwardsToTheVerbatimTarget(t *testing.T) {
	clientKey := newTestSigner(t)

	var (
		mu     sync.Mutex
		dialed []string
	)

	addr, hostKey := startSSHProxy(t, clientKey, func(_ context.Context, _, target string) (net.Conn, error) {
		mu.Lock()

		dialed = append(dialed, target)
		mu.Unlock()

		upstream, backend := net.Pipe()

		// the backend echoes, so a round trip proves both directions are relayed
		go func() {
			defer backend.Close()

			_, _ = io.Copy(backend, backend)
		}()

		return upstream, nil
	})

	client, err := dialSSH(t, addr, hostKey, clientKey)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer client.Close()

	conn, err := client.Dial("tcp", "postgres-rw.db.staging:5432")
	if err != nil {
		t.Fatalf("opening direct-tcpip channel: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(buf) != "ping" {
		t.Errorf("echo = %q, want %q", buf, "ping")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(dialed) != 1 || dialed[0] != "postgres-rw.db.staging:5432" {
		t.Errorf("dialed = %v, want the cluster address unresolved", dialed)
	}
}

func TestSSHProxyRejectsAnUnknownKey(t *testing.T) {
	addr, hostKey := startSSHProxy(t, newTestSigner(t), func(context.Context, string, string) (net.Conn, error) {
		t.Error("DialContext must not be called for an unauthenticated client")
		return nil, errors.New("unreachable")
	})

	client, err := dialSSH(t, addr, hostKey, newTestSigner(t))
	if err == nil {
		client.Close()
		t.Fatal("ssh dial with an unauthorized key should fail")
	}
}

func TestSSHProxyRefusesSessionsAndRemoteForwarding(t *testing.T) {
	clientKey := newTestSigner(t)
	addr, hostKey := startSSHProxy(t, clientKey, func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unreachable")
	})

	client, err := dialSSH(t, addr, hostKey, clientKey)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer client.Close()

	if session, err := client.NewSession(); err == nil {
		session.Close()
		t.Error("opening a session (shell, exec, sftp) should be refused")
	}

	if l, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
		l.Close()
		t.Error("remote forwarding should be refused")
	}
}

func TestSSHProxyRejectsTheChannelWhenTheDialFails(t *testing.T) {
	clientKey := newTestSigner(t)
	addr, hostKey := startSSHProxy(t, clientKey, func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no ready endpoints")
	})

	client, err := dialSSH(t, addr, hostKey, clientKey)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer client.Close()

	_, err = client.Dial("tcp", "missing.db.staging:5432")
	if err == nil {
		t.Fatal("opening a channel to an unreachable target should fail")
	}

	if !strings.Contains(err.Error(), "no ready endpoints") {
		t.Errorf("error = %v, want the dial error passed to the client", err)
	}
}

func TestSSHProxyServeAfterClose(t *testing.T) {
	p := &SSHProxy{Config: NewSSHServerConfig(newTestSigner(t), nil)}

	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	if err := p.Serve(l); err != nil {
		t.Errorf("serve after close = %v, want nil", err)
	}
}

func TestLoadOrCreateHostKeyPersistsTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "ssh_host_ed25519_key")

	first, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("creating host key: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat host key: %v", err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("host key mode = %o, want 600", mode)
	}

	second, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("loading host key: %v", err)
	}

	if !bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) {
		t.Error("the host key changed between starts")
	}
}

func TestLoadOrCreateHostKeyRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh_host_ed25519_key")
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreateHostKey(path); err == nil {
		t.Error("an unparsable host key file should be an error, not silently replaced")
	}
}

func TestLoadAuthorizedKeys(t *testing.T) {
	dir := t.TempDir()
	a, b := newTestSigner(t), newTestSigner(t)

	authorized := "# team keys\n\n" +
		string(ssh.MarshalAuthorizedKey(a.PublicKey())) +
		"not a key line\n" +
		string(ssh.MarshalAuthorizedKey(b.PublicKey()))

	keysFile := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(keysFile, []byte(authorized), 0o600); err != nil {
		t.Fatal(err)
	}

	keys, err := LoadAuthorizedKeys([]string{keysFile})
	if err != nil {
		t.Fatalf("loading keys: %v", err)
	}

	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}

	emptyFile := filepath.Join(dir, "empty.pub")
	if err := os.WriteFile(emptyFile, []byte("# nothing here\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadAuthorizedKeys([]string{emptyFile}); err == nil {
		t.Error("files without any key should be an error")
	}

	if _, err := LoadAuthorizedKeys(nil); err == nil {
		t.Error("no files should be an error")
	}

	if _, err := LoadAuthorizedKeys([]string{filepath.Join(dir, "missing.pub")}); err == nil {
		t.Error("a missing file should be an error")
	}
}

func TestSSHProxyKeepsTheReplyAfterTheClientHalfCloses(t *testing.T) {
	clientKey := newTestSigner(t)
	proxyUpstreamSide, upstream := tcpPair(t)

	go func() {
		request, _ := io.ReadAll(upstream)
		_, _ = upstream.Write([]byte("response to " + string(request)))
		upstream.Close()
	}()

	addr, hostKey := startSSHProxy(t, clientKey, func(context.Context, string, string) (net.Conn, error) {
		return proxyUpstreamSide, nil
	})

	client, err := dialSSH(t, addr, hostKey, clientKey)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer client.Close()

	conn, err := client.Dial("tcp", "api.web.staging:80")
	if err != nil {
		t.Fatalf("opening direct-tcpip channel: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("request")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// the channel sends EOF the way ssh -L does when the local client closes its write side
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}

	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(reply) != "response to request" {
		t.Errorf("reply = %q, want the response sent after the client's EOF", reply)
	}
}

func TestSSHProxyDropsAClientThatNeverAuthenticates(t *testing.T) {
	p := &SSHProxy{
		Config:           NewSSHServerConfig(newTestSigner(t), nil),
		HandshakeTimeout: 200 * time.Millisecond,
	}

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	go func() { _ = p.Serve(l) }()

	t.Cleanup(func() { p.Close() })

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	// the client sends nothing; the server's banner arrives, then the connection must end
	if _, err := io.ReadAll(conn); err != nil {
		t.Errorf("the silent client was not dropped: %v", err)
	}
}

func TestSSHProxyCancelsTheDialWhenTheClientLeaves(t *testing.T) {
	clientKey := newTestSigner(t)
	started := make(chan struct{})
	cancelled := make(chan struct{})

	addr, hostKey := startSSHProxy(t, clientKey, func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)

		return nil, ctx.Err()
	})

	client, err := dialSSH(t, addr, hostKey, clientKey)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}

	go func() { _, _ = client.Dial("tcp", "slow.db.staging:5432") }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial never started")
	}

	client.Close()

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial was not cancelled after the client disconnected")
	}
}

func TestLoadAuthorizedKeysRefusesKeyOptions(t *testing.T) {
	key := newTestSigner(t)

	for _, options := range []string{`from="10.0.0.0/8"`, `restrict,permitopen="db:5432"`, "no-port-forwarding"} {
		path := filepath.Join(t.TempDir(), "authorized_keys")
		line := options + " " + string(ssh.MarshalAuthorizedKey(key.PublicKey()))

		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := LoadAuthorizedKeys([]string{path}); err == nil {
			t.Errorf("a key with %s should be refused, podproxy cannot enforce it", options)
		}
	}
}

// closeTrackingConn reports when the proxy fully closes the upstream.
type closeTrackingConn struct {
	*net.TCPConn

	once   sync.Once
	closed chan struct{}
}

func (c *closeTrackingConn) Close() error {
	c.once.Do(func() { close(c.closed) })

	return c.TCPConn.Close()
}

func TestSSHProxyClosesTheUpstreamWhenTheClientConnectionEnds(t *testing.T) {
	clientKey := newTestSigner(t)
	proxyUpstreamSide, upstream := tcpPair(t)
	tracked := &closeTrackingConn{TCPConn: proxyUpstreamSide, closed: make(chan struct{})}

	// the upstream stays idle and never answers the EOF
	go func() { _, _ = io.Copy(io.Discard, upstream) }()

	addr, hostKey := startSSHProxy(t, clientKey, func(context.Context, string, string) (net.Conn, error) {
		return tracked, nil
	})

	client, err := dialSSH(t, addr, hostKey, clientKey)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}

	conn, err := client.Dial("tcp", "idle.db.staging:5432")
	if err != nil {
		t.Fatalf("opening direct-tcpip channel: %v", err)
	}
	defer conn.Close()

	client.Close()

	select {
	case <-tracked.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream stayed open after the ssh connection ended")
	}
}

func TestSSHProxyClosesTheUpstreamWhenTheChannelCloses(t *testing.T) {
	clientKey := newTestSigner(t)
	proxyUpstreamSide, upstream := tcpPair(t)
	tracked := &closeTrackingConn{TCPConn: proxyUpstreamSide, closed: make(chan struct{})}

	// the upstream stays idle and never answers the EOF
	go func() { _, _ = io.Copy(io.Discard, upstream) }()

	addr, hostKey := startSSHProxy(t, clientKey, func(context.Context, string, string) (net.Conn, error) {
		return tracked, nil
	})

	client, err := dialSSH(t, addr, hostKey, clientKey)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer client.Close()

	conn, err := client.Dial("tcp", "idle.db.staging:5432")
	if err != nil {
		t.Fatalf("opening direct-tcpip channel: %v", err)
	}

	// closing one channel while the ssh connection stays up, as a finished forward of ssh -L does
	conn.Close()

	select {
	case <-tracked.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream stayed open after its channel was closed")
	}
}

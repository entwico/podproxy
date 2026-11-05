package proxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const defaultSSHHandshakeTimeout = 10 * time.Second

// SSHProxy is an SSH server that only opens direct-tcpip channels: the target
// host of each channel is handed verbatim to DialContext, so cluster addresses
// resolve exactly as through the SOCKS5 proxy. Sessions (shell, exec, sftp) and
// remote forwarding are refused. Tools that tunnel over SSH (ssh -L, ProxyJump,
// database clients with an SSH tunnel) can use it in place of SOCKS5.
type SSHProxy struct {
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	Config      *ssh.ServerConfig
	Logger      *slog.Logger
	// HandshakeTimeout bounds how long a client may take to authenticate; zero means 10s
	HandshakeTimeout time.Duration

	mu       sync.Mutex
	listener net.Listener
	closed   bool
}

// directTCPIPPayload is the extra data of a direct-tcpip channel open request (RFC 4254 7.2).
type directTCPIPPayload struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

// NewSSHServerConfig returns a server config that authenticates clients by
// public key only, against the given authorized keys; the user name is ignored.
func NewSSHServerConfig(hostKey ssh.Signer, authorizedKeys []ssh.PublicKey) *ssh.ServerConfig {
	authorized := make(map[string]bool, len(authorizedKeys))
	for _, k := range authorizedKeys {
		authorized[string(k.Marshal())] = true
	}

	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-podproxy",
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !authorized[string(key.Marshal())] {
				return nil, errors.New("public key not authorized")
			}

			return &ssh.Permissions{}, nil
		},
	}

	cfg.AddHostKey(hostKey)

	return cfg
}

// ListenAndServe listens on addr and serves SSH connections until Close is called.
func (p *SSHProxy) ListenAndServe(ctx context.Context, addr string) error {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	return p.Serve(l)
}

// Serve accepts connections on l until Close is called. It returns nil after Close.
func (p *SSHProxy) Serve(l net.Listener) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		l.Close()

		return nil
	}

	p.listener = l
	p.mu.Unlock()

	for {
		conn, err := l.Accept()
		if err != nil {
			if p.isClosed() {
				return nil
			}

			return fmt.Errorf("accepting ssh connection: %w", err)
		}

		go p.handleConn(conn)
	}
}

// Close stops accepting connections. Established connections run until their clients close them.
func (p *SSHProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	if p.listener == nil {
		return nil
	}

	return p.listener.Close()
}

func (p *SSHProxy) handshakeTimeout() time.Duration {
	if p.HandshakeTimeout > 0 {
		return p.HandshakeTimeout
	}

	return defaultSSHHandshakeTimeout
}

func (p *SSHProxy) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.closed
}

func (p *SSHProxy) handleConn(conn net.Conn) {
	defer conn.Close()

	// a client that never finishes the handshake must not hold the connection open
	if err := conn.SetDeadline(time.Now().Add(p.handshakeTimeout())); err != nil {
		p.logDebug("setting handshake deadline failed", "error", err)
		return
	}

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, p.Config)
	if err != nil {
		p.logDebug("ssh handshake failed", "remote", conn.RemoteAddr(), "error", err)
		return
	}
	defer sshConn.Close()

	if err := conn.SetDeadline(time.Time{}); err != nil {
		p.logDebug("clearing handshake deadline failed", "error", err)
		return
	}

	// dials still in flight are cancelled once the client goes away
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go rejectGlobalRequests(reqs)

	var channels sync.WaitGroup

	for newCh := range chans {
		if newCh.ChannelType() != "direct-tcpip" {
			p.reject(newCh, ssh.UnknownChannelType, "only direct-tcpip channels are supported")
			continue
		}

		channels.Go(func() {
			p.handleDirectTCPIP(ctx, newCh)
		})
	}

	cancel()
	channels.Wait()
}

func (p *SSHProxy) handleDirectTCPIP(ctx context.Context, newCh ssh.NewChannel) {
	var payload directTCPIPPayload
	if err := ssh.Unmarshal(newCh.ExtraData(), &payload); err != nil {
		p.reject(newCh, ssh.ConnectionFailed, "malformed direct-tcpip request")
		return
	}

	addr := net.JoinHostPort(payload.Host, strconv.FormatUint(uint64(payload.Port), 10))

	upstream, err := p.DialContext(ctx, "tcp", addr)
	if err != nil {
		p.logError("dial upstream failed", "addr", addr, "error", err)
		p.reject(newCh, ssh.ConnectionFailed, fmt.Sprintf("dial %s: %v", addr, err))

		return
	}
	defer upstream.Close()

	// a lost ssh connection reaches the channel as a clean EOF, which only
	// half-closes the upstream; closing it once the connection is gone ends
	// the relay even when the upstream never answers the EOF
	stop := context.AfterFunc(ctx, func() { upstream.Close() })
	defer stop()

	ch, chReqs, err := newCh.Accept()
	if err != nil {
		p.logDebug("accepting channel failed", "addr", addr, "error", err)
		return
	}
	defer ch.Close()

	// a channel close reaches the relay as a plain EOF, which only half-closes
	// the upstream; chReqs ends on close but not on EOF, so it tells the two apart
	go func() {
		ssh.DiscardRequests(chReqs)
		upstream.Close()
	}()

	p.logDebug("ssh channel opened", "addr", addr)

	relay(ch, upstream)
}

func (p *SSHProxy) reject(newCh ssh.NewChannel, reason ssh.RejectionReason, message string) {
	if err := newCh.Reject(reason, message); err != nil {
		p.logDebug("rejecting channel failed", "error", err)
	}
}

// rejectGlobalRequests refuses every global request, remote forwarding
// (tcpip-forward) included; keepalives get a failure reply, which clients
// accept as a sign of life.
func rejectGlobalRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
	}
}

// LoadOrCreateHostKey reads the ed25519 host key at path, or generates and
// stores one when the file does not exist, so the key (and the fingerprint
// clients pin) survives restarts.
func LoadOrCreateHostKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("parsing ssh host key %q: %w", path, err)
		}

		return signer, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading ssh host key %q: %w", path, err)
	}

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating ssh host key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(key, "podproxy host key")
	if err != nil {
		return nil, fmt.Errorf("encoding ssh host key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating ssh host key directory: %w", err)
	}

	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, fmt.Errorf("writing ssh host key %q: %w", path, err)
	}

	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, fmt.Errorf("creating ssh host key signer: %w", err)
	}

	return signer, nil
}

// LoadAuthorizedKeys parses public keys in authorized_keys format (which
// includes *.pub files) from the given files. At least one key is required.
// Key options (from=, permitopen=, restrict, ...) are refused rather than
// ignored: the proxy could not enforce them, and ignoring them would grant the
// forwarding they forbid.
func LoadAuthorizedKeys(paths []string) ([]ssh.PublicKey, error) {
	var keys []ssh.PublicKey

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading authorized keys %q: %w", path, err)
		}

		// ParseAuthorizedKey skips comments and lines it cannot parse; an error means no key is left
		for len(data) > 0 {
			key, comment, options, rest, err := ssh.ParseAuthorizedKey(data)
			if err != nil {
				break
			}

			if len(options) > 0 {
				return nil, fmt.Errorf("authorized key %q in %q has options %v, which podproxy cannot enforce", comment, path, options)
			}

			keys = append(keys, key)
			data = rest
		}
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("no public keys found in %v", paths)
	}

	return keys, nil
}

func (p *SSHProxy) logError(msg string, args ...any) {
	if p.Logger != nil {
		p.Logger.Error(msg, args...)
	}
}

func (p *SSHProxy) logDebug(msg string, args ...any) {
	if p.Logger != nil {
		p.Logger.Debug(msg, args...)
	}
}

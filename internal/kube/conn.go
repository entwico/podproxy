package kube

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
)

// StreamConn wraps a pair of SPDY streams (data + error) as a net.Conn.
// It is safe for concurrent use by multiple goroutines.
type StreamConn struct {
	dataStream   httpstream.Stream
	errorStream  httpstream.Stream
	spdyConn     httpstream.Connection
	remoteTarget string

	closeOnce   sync.Once
	remoteErrMu sync.Mutex
	remoteErr   error
	errDone     chan struct{}

	createdAt    time.Time
	bytesRead    atomic.Int64
	bytesWritten atomic.Int64
}

// NewStreamConn creates a StreamConn that reads/writes via the data stream and
// monitors the error stream for remote errors in a background goroutine.
func NewStreamConn(data, errStream httpstream.Stream, conn httpstream.Connection, target string) *StreamConn {
	sc := &StreamConn{
		dataStream:   data,
		errorStream:  errStream,
		spdyConn:     conn,
		remoteTarget: target,
		errDone:      make(chan struct{}),
		createdAt:    time.Now(),
	}
	go sc.monitorErrors()

	return sc
}

func (sc *StreamConn) Read(b []byte) (int, error) {
	n, err := sc.dataStream.Read(b)
	sc.bytesRead.Add(int64(n))

	if err == io.EOF {
		// wait for the error monitor to finish, with a timeout to prevent
		// deadlock if monitorErrors is stuck or the SPDY connection misbehaves.
		select {
		case <-sc.errDone:
		case <-time.After(5 * time.Second):
			return n, err
		}

		sc.remoteErrMu.Lock()
		remoteErr := sc.remoteErr
		sc.remoteErrMu.Unlock()

		if remoteErr != nil {
			return n, remoteErr
		}
	}

	return n, err
}

func (sc *StreamConn) Write(b []byte) (int, error) {
	n, err := sc.dataStream.Write(b)
	sc.bytesWritten.Add(int64(n))

	return n, err
}

func (sc *StreamConn) BytesRead() int64        { return sc.bytesRead.Load() }
func (sc *StreamConn) BytesWritten() int64     { return sc.bytesWritten.Load() }
func (sc *StreamConn) Duration() time.Duration { return time.Since(sc.createdAt) }

func (sc *StreamConn) Close() error {
	var err error

	sc.closeOnce.Do(func() {
		// close the data stream first so the remote side sees EOF.
		err = sc.dataStream.Close()
		// explicitly close the error stream before the SPDY connection to
		// ensure resources are released in order.
		if closeErr := sc.errorStream.Close(); err == nil {
			err = closeErr
		}
		// close the SPDY connection to release remaining resources and its
		// monitoring goroutine, preventing a connection and goroutine leak.
		sc.spdyConn.Close()
	})

	return err
}

func (sc *StreamConn) LocalAddr() net.Addr {
	// return a *net.TCPAddr so the go-socks5 SendReply recognizes the address
	// type and doesn't respond with RepAddrTypeNotSupported.
	return &net.TCPAddr{IP: net.IPv4zero, Port: 0}
}

func (sc *StreamConn) RemoteAddr() net.Addr {
	return stubAddr(sc.remoteTarget)
}

// SetDeadline is a no-op — SPDY streams do not support deadlines.
func (sc *StreamConn) SetDeadline(_ time.Time) error      { return nil }
func (sc *StreamConn) SetReadDeadline(_ time.Time) error  { return nil }
func (sc *StreamConn) SetWriteDeadline(_ time.Time) error { return nil }

func (sc *StreamConn) monitorErrors() {
	defer close(sc.errDone)

	// cap the read to prevent unbounded memory usage from a large error response.
	const maxErrorBytes = 4096

	buf, err := io.ReadAll(io.LimitReader(sc.errorStream, maxErrorBytes))

	sc.remoteErrMu.Lock()
	defer sc.remoteErrMu.Unlock()

	if err != nil {
		sc.remoteErr = fmt.Errorf("reading error stream: %w", err)
		return
	}

	if len(buf) > 0 {
		sc.remoteErr = fmt.Errorf("remote error: %s", string(buf))
	}
}

// stubAddr implements net.Addr with a fixed string.
type stubAddr string

func (s stubAddr) Network() string { return "spdy" }
func (s stubAddr) String() string  { return string(s) }

// verify StreamConn satisfies net.Conn.
var _ net.Conn = (*StreamConn)(nil)

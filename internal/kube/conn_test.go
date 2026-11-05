package kube

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
)

// fakeStream records writes and closes; it has nothing to read.
type fakeStream struct {
	mu     sync.Mutex
	closes int
	buf    bytes.Buffer
}

func (s *fakeStream) Read(_ []byte) (int, error) { return 0, io.EOF }

func (s *fakeStream) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.buf.Write(b)
}

func (s *fakeStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closes++

	return nil
}

func (s *fakeStream) Reset() error         { return nil }
func (s *fakeStream) Headers() http.Header { return http.Header{} }
func (s *fakeStream) Identifier() uint32   { return 1 }

func (s *fakeStream) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closes
}

type fakeConnection struct{ closed chan bool }

func (c *fakeConnection) CreateStream(http.Header) (httpstream.Stream, error) { return nil, nil }
func (c *fakeConnection) Close() error                                        { return nil }
func (c *fakeConnection) CloseChan() <-chan bool                              { return c.closed }
func (c *fakeConnection) SetIdleTimeout(time.Duration)                        {}
func (c *fakeConnection) RemoveStreams(...httpstream.Stream)                  {}

func TestStreamConnCloseWriteEndsOnlyTheDataStream(t *testing.T) {
	data := &fakeStream{}
	errStream := &fakeStream{}
	sc := NewStreamConn(data, errStream, &fakeConnection{closed: make(chan bool)}, "pod:5432")

	if err := sc.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() error: %v", err)
	}

	if err := sc.CloseWrite(); err != nil {
		t.Fatalf("second CloseWrite() error: %v", err)
	}

	if got := data.closeCount(); got != 1 {
		t.Errorf("data stream closes after CloseWrite = %d, want 1", got)
	}

	if got := errStream.closeCount(); got != 0 {
		t.Errorf("error stream closes after CloseWrite = %d, want 0: half-close must keep the connection", got)
	}

	if err := sc.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	if got := data.closeCount(); got != 1 {
		t.Errorf("data stream closes after Close = %d, want 1: an ended stream is not ended twice", got)
	}

	if got := errStream.closeCount(); got != 1 {
		t.Errorf("error stream closes after Close = %d, want 1", got)
	}
}

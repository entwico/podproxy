package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHTTPProxyNonAbsoluteURL(t *testing.T) {
	proxy := &HTTPProxy{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			t.Fatal("DialContext should not be called for non-absolute URLs")
			return nil, nil
		},
	}

	rec := httptest.NewRecorder()
	// non-absolute URL (relative path only)
	req := httptest.NewRequest(http.MethodGet, "/relative-path", nil)

	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHTTPConnectDialFailure(t *testing.T) {
	proxy := &HTTPProxy{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("connection refused")
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodConnect, "example.com:443", nil)

	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestHTTPConnectSuccess(t *testing.T) {
	// upstream is the mock backend; serverConn is what the proxy writes to
	upstreamClient, serverConn := net.Pipe()

	proxy := &HTTPProxy{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return serverConn, nil
		},
	}

	ts := httptest.NewServer(proxy)
	defer ts.Close()

	// connect to the test server with a raw TCP connection and send a CONNECT request
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	_, err = fmt.Fprint(conn, "CONNECT target.example.com:443 HTTP/1.1\r\nHost: target.example.com:443\r\n\r\n")
	if err != nil {
		t.Fatalf("write CONNECT request: %v", err)
	}

	br := bufio.NewReader(conn)

	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// verify bidirectional data flow
	const (
		clientMsg = "hello from client"
		serverMsg = "hello from server"
	)

	// client → upstream

	if _, err := fmt.Fprint(conn, clientMsg); err != nil {
		t.Fatalf("client write: %v", err)
	}

	buf := make([]byte, len(clientMsg))
	if _, err := upstreamClient.Read(buf); err != nil {
		t.Fatalf("upstream read: %v", err)
	}

	if string(buf) != clientMsg {
		t.Errorf("upstream received %q, want %q", string(buf), clientMsg)
	}

	// upstream → client
	if _, err := fmt.Fprint(upstreamClient, serverMsg); err != nil {
		t.Fatalf("upstream write: %v", err)
	}

	buf = make([]byte, len(serverMsg))
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("client read: %v", err)
	}

	if got := strings.TrimSpace(string(buf)); got != serverMsg {
		t.Errorf("client received %q, want %q", got, serverMsg)
	}
}

func TestHTTPProxyForwardGET(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Custom", "from-backend")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "hello from backend")
	}))
	defer backend.Close()

	proxy := &HTTPProxy{
		DialContext: (&net.Dialer{}).DialContext,
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, backend.URL+"/test", nil)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if got := resp.Header.Get("X-Custom"); got != "from-backend" {
		t.Errorf("X-Custom = %q, want %q", got, "from-backend")
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from backend" {
		t.Errorf("body = %q, want %q", string(body), "hello from backend")
	}
}

func TestHTTPProxyForwardPOST(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "received: %s", body)
	}))
	defer backend.Close()

	proxy := &HTTPProxy{
		DialContext: (&net.Dialer{}).DialContext,
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, backend.URL+"/submit", strings.NewReader("request body"))
	req.Header.Set("Content-Type", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST through proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "received: request body" {
		t.Errorf("body = %q, want %q", string(body), "received: request body")
	}
}

func TestHTTPProxyHopByHopHeaders(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify hop-by-hop headers were stripped from the forwarded request
		if got := r.Header.Get("Proxy-Authorization"); got != "" {
			t.Errorf("Proxy-Authorization should be stripped, got %q", got)
		}

		if got := r.Header.Get("Connection"); got != "" {
			t.Errorf("Connection should be stripped, got %q", got)
		}

		// send hop-by-hop headers in the response to verify they get stripped
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("Proxy-Authenticate", "Basic")
		w.Header().Set("X-Real-Header", "preserved")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	proxy := &HTTPProxy{
		DialContext: (&net.Dialer{}).DialContext,
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, backend.URL+"/test", nil)
	req.Header.Set("Proxy-Authorization", "Basic secret")
	req.Header.Set("Connection", "keep-alive")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Keep-Alive"); got != "" {
		t.Errorf("Keep-Alive should be stripped from response, got %q", got)
	}

	if got := resp.Header.Get("Proxy-Authenticate"); got != "" {
		t.Errorf("Proxy-Authenticate should be stripped from response, got %q", got)
	}

	if got := resp.Header.Get("X-Real-Header"); got != "preserved" {
		t.Errorf("X-Real-Header = %q, want %q", got, "preserved")
	}
}

func TestHTTPProxyForwardDialFailure(t *testing.T) {
	proxy := &HTTPProxy{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, errors.New("connection refused")
		},
	}

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unreachable.example.com/test", nil)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
}

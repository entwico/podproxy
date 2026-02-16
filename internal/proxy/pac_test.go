package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeneratePACMultipleClusters(t *testing.T) {
	s := &PACServer{
		ClusterNames: []string{"production", "staging", "dev"},
		SOCKSAddress: "127.0.0.1:1080",
	}

	pac := s.generatePAC()

	for _, name := range s.ClusterNames {
		if !strings.Contains(pac, "*."+name) {
			t.Errorf("PAC should contain condition for cluster %q", name)
		}
	}

	if !strings.Contains(pac, "FindProxyForURL") {
		t.Error("PAC should contain FindProxyForURL function")
	}

	if !strings.Contains(pac, "DIRECT") {
		t.Error("PAC should contain DIRECT fallback")
	}
}

func TestGeneratePACWithHTTPProxy(t *testing.T) {
	s := &PACServer{
		ClusterNames:     []string{"production"},
		SOCKSAddress:     "127.0.0.1:1080",
		HTTPProxyAddress: "127.0.0.1:1081",
	}

	pac := s.generatePAC()

	if !strings.Contains(pac, "PROXY 127.0.0.1:1081") {
		t.Error("PAC should contain PROXY directive for HTTP proxy address")
	}

	if !strings.Contains(pac, "SOCKS5 127.0.0.1:1080") {
		t.Error("PAC should contain SOCKS5 directive as fallback")
	}
}

func TestGeneratePACSOCKS5Only(t *testing.T) {
	s := &PACServer{
		ClusterNames: []string{"production"},
		SOCKSAddress: "127.0.0.1:1080",
	}

	pac := s.generatePAC()

	if strings.Contains(pac, "PROXY ") {
		t.Error("PAC should not contain PROXY directive when HTTP proxy is not configured")
	}

	if !strings.Contains(pac, "SOCKS5 127.0.0.1:1080") {
		t.Error("PAC should contain SOCKS5 directive")
	}
}

func TestPACServerHTTPHandler(t *testing.T) {
	s := &PACServer{
		ClusterNames: []string{"production", "staging"},
		SOCKSAddress: "127.0.0.1:1080",
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/proxy.pac", nil)

	s.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "application/x-ns-proxy-autoconfig" {
		t.Errorf("Content-Type = %q, want %q", got, "application/x-ns-proxy-autoconfig")
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "FindProxyForURL") {
		t.Error("response body should contain PAC function")
	}
}

package podproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const samplePac = `function FindProxyForURL(url, host) {
  if (shExpMatch(host, "*.staging"))
    return "PROXY 127.0.0.1:9081; SOCKS5 127.0.0.1:9080; DIRECT";
  if (shExpMatch(host, "*.prod-cluster"))
    return "PROXY 127.0.0.1:9081; SOCKS5 127.0.0.1:9080; DIRECT";
  return "DIRECT";
}
`

func TestParsePacPatterns(t *testing.T) {
	patterns := parsePacPatterns(samplePac)

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(patterns))
	}

	cases := []struct {
		host    string
		matches bool
	}{
		{"redis.staging", true},
		{"postgres.db.staging", true},
		{"redis-0.redis.cache.staging", true},
		{"api.prod-cluster", true},
		{"staging", false},
		{"redis.stagingx", false},
		{"redis.staging.example.com", false},
		{"example.com", false},
	}

	for _, tc := range cases {
		matched := false

		for _, pattern := range patterns {
			if pattern.MatchString(tc.host) {
				matched = true

				break
			}
		}

		if matched != tc.matches {
			t.Errorf("host %q: expected match=%v, got %v", tc.host, tc.matches, matched)
		}
	}
}

func TestParsePacPatternsEmpty(t *testing.T) {
	pac := "function FindProxyForURL(url, host) {\n  return \"DIRECT\";\n}\n"

	if patterns := parsePacPatterns(pac); len(patterns) != 0 {
		t.Fatalf("expected no patterns, got %d", len(patterns))
	}
}

func TestLoadPatternsFromPacServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(samplePac))
	}))
	defer server.Close()

	patterns := loadPatterns("", server.URL, &logger{})

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(patterns))
	}
}

func TestLoadPatternsUnreachablePac(t *testing.T) {
	patterns := loadPatterns(`\.local-cluster$`, "http://127.0.0.1:1", &logger{})

	if len(patterns) != 1 {
		t.Fatalf("expected only the match pattern, got %d patterns", len(patterns))
	}

	if !patterns[0].MatchString("redis.local-cluster") {
		t.Error("match pattern should match redis.local-cluster")
	}
}

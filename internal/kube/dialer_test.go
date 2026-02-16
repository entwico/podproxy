package kube

import "testing"

func TestClusterSuffix(t *testing.T) {
	dialer := &ClusterDialer{
		Forwarders: map[string]*PortForwarder{
			"production": {},
			"staging":    {},
		},
	}

	tests := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "known cluster two parts",
			addr: "redis.production:6379",
			want: "production",
		},
		{
			name: "known cluster three parts",
			addr: "redis.default.staging:6379",
			want: "staging",
		},
		{
			name: "known cluster with svc suffix",
			addr: "redis.production.svc:6379",
			want: "production",
		},
		{
			name: "known cluster with svc.cluster.local suffix",
			addr: "redis.default.production.svc.cluster.local:6379",
			want: "production",
		},
		{
			name: "unknown cluster suffix",
			addr: "redis.unknown:6379",
			want: "",
		},
		{
			name: "plain hostname passthrough",
			addr: "example.com:443",
			want: "",
		},
		{
			name: "multi-segment hostname passthrough",
			addr: "api.github.com:443",
			want: "",
		},
		{
			name: "single-part hostname passthrough",
			addr: "localhost:8080",
			want: "",
		},
		{
			name: "missing port",
			addr: "redis.production",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dialer.clusterSuffix(tt.addr)
			if got != tt.want {
				t.Errorf("clusterSuffix(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

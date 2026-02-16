package kube

import "testing"

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		wantCluster string
		wantService bool
		wantSvcName string
		wantPod     string
		wantNS      string
		wantPort    int
	}{
		{
			name:        "two parts: service in default namespace",
			addr:        "redis.production:6379",
			wantCluster: "production",
			wantService: true,
			wantSvcName: "redis",
			wantPort:    6379,
		},
		{
			name:        "three parts: service in explicit namespace",
			addr:        "mongodb-svc.databases.staging:27017",
			wantCluster: "staging",
			wantService: true,
			wantSvcName: "mongodb-svc",
			wantNS:      "databases",
			wantPort:    27017,
		},
		{
			name:        "four parts: direct pod",
			addr:        "mongo-0.mongodb-svc.databases.staging:27017",
			wantCluster: "staging",
			wantService: false,
			wantSvcName: "mongodb-svc",
			wantPod:     "mongo-0",
			wantNS:      "databases",
			wantPort:    27017,
		},
		{
			name:        "strips .svc.cluster.local suffix",
			addr:        "redis.default.production.svc.cluster.local:6379",
			wantCluster: "production",
			wantService: true,
			wantSvcName: "redis",
			wantNS:      "default",
			wantPort:    6379,
		},
		{
			name:        "strips .svc suffix",
			addr:        "redis.production.svc:6379",
			wantCluster: "production",
			wantService: true,
			wantSvcName: "redis",
			wantPort:    6379,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := ParseTarget(tt.addr)
			if err != nil {
				t.Fatalf("ParseTarget(%q) error: %v", tt.addr, err)
			}

			if target.Cluster != tt.wantCluster {
				t.Errorf("Cluster = %q, want %q", target.Cluster, tt.wantCluster)
			}

			if target.IsService != tt.wantService {
				t.Errorf("IsService = %v, want %v", target.IsService, tt.wantService)
			}

			if target.ServiceName != tt.wantSvcName {
				t.Errorf("ServiceName = %q, want %q", target.ServiceName, tt.wantSvcName)
			}

			if target.PodName != tt.wantPod {
				t.Errorf("PodName = %q, want %q", target.PodName, tt.wantPod)
			}

			if target.Namespace != tt.wantNS {
				t.Errorf("Namespace = %q, want %q", target.Namespace, tt.wantNS)
			}

			if target.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", target.Port, tt.wantPort)
			}
		})
	}
}

func TestParseTargetErrors(t *testing.T) {
	tests := []struct {
		name string
		addr string
	}{
		{"single-part hostname", "redis:6379"},
		{"five-part hostname", "a.b.c.d.e:6379"},
		{"non-numeric port", "redis.production:abc"},
		{"missing port", "redis.production"},
		{"port zero", "redis.production:0"},
		{"negative port", "redis.production:-1"},
		{"port too large", "redis.production:65536"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTarget(tt.addr)
			if err == nil {
				t.Errorf("ParseTarget(%q) should have failed", tt.addr)
			}
		})
	}
}

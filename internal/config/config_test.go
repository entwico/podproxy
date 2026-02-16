package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKubeconfig creates a minimal kubeconfig file with the given context→namespace mappings.
func writeKubeconfig(t *testing.T, dir, filename string, contexts map[string]string) string {
	t.Helper()

	path := filepath.Join(dir, filename)

	var content strings.Builder
	content.WriteString("apiVersion: v1\nkind: Config\nclusters:\n")

	for name := range contexts {
		content.WriteString(fmt.Sprintf("- cluster:\n    server: https://%s.example.com\n  name: %s\n", name, name))
	}

	content.WriteString("contexts:\n")

	for name, ns := range contexts {
		content.WriteString(fmt.Sprintf("- context:\n    cluster: %s\n    user: %s\n", name, name))

		if ns != "" {
			content.WriteString(fmt.Sprintf("    namespace: %s\n", ns))
		}

		content.WriteString(fmt.Sprintf("  name: %s\n", name))
	}

	content.WriteString("users:\n")

	for name := range contexts {
		content.WriteString(fmt.Sprintf("- name: %s\n  user:\n    token: fake-token\n", name))
	}

	if err := os.WriteFile(path, []byte(content.String()), 0600); err != nil {
		t.Fatalf("writing kubeconfig: %v", err)
	}

	return path
}

const testClusterProduction = "production"

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	kc1 := writeKubeconfig(t, dir, "cluster1.yaml", map[string]string{
		testClusterProduction: testClusterProduction,
	})
	kc2 := writeKubeconfig(t, dir, "cluster2.yaml", map[string]string{
		"staging": "staging",
	})

	configContent := fmt.Sprintf(`
listenAddress: "0.0.0.0:1080"
kubeconfigs:
  - %q
  - %q
`, kc1, kc2)

	cfgPath := writeTempConfig(t, configContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.ListenAddress != "0.0.0.0:1080" {
		t.Errorf("ListenAddress = %q, want %q", cfg.ListenAddress, "0.0.0.0:1080")
	}

	if len(cfg.ResolvedClusters) != 2 {
		t.Fatalf("len(ResolvedClusters) = %d, want 2", len(cfg.ResolvedClusters))
	}

	// find the production cluster
	found := false

	for _, rc := range cfg.ResolvedClusters {
		if rc.Name == testClusterProduction {
			found = true

			if rc.Namespace != testClusterProduction {
				t.Errorf("production.Namespace = %q, want %q", rc.Namespace, testClusterProduction)
			}

			if rc.Context != testClusterProduction {
				t.Errorf("production.Context = %q, want %q", rc.Context, testClusterProduction)
			}

			if rc.Kubeconfig != kc1 {
				t.Errorf("production.Kubeconfig = %q, want %q", rc.Kubeconfig, kc1)
			}
		}
	}

	if !found {
		t.Error("expected to find resolved cluster named 'production'")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	dir := t.TempDir()

	// create a kubeconfig so validation passes
	writeKubeconfig(t, dir, "test.yaml", map[string]string{"ctx": "default"})

	// patch defaults to point at the temp dir so resolve() finds the kubeconfig
	origDefaults := DefaultConfigData

	t.Cleanup(func() { DefaultConfigData = origDefaults })

	DefaultConfigData = fmt.Appendf(nil, "listenAddress: \"127.0.0.1:9080\"\nkubeconfigs:\n  - %q\n", filepath.Join(dir, "*.yaml"))

	cfg, err := LoadConfig(filepath.Join(dir, "nonexistent.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig() should not fail for missing config file, got: %v", err)
	}

	if cfg.ListenAddress != "127.0.0.1:9080" {
		t.Errorf("ListenAddress = %q, want default %q", cfg.ListenAddress, "127.0.0.1:9080")
	}

	if len(cfg.ResolvedClusters) != 1 {
		t.Fatalf("len(ResolvedClusters) = %d, want 1", len(cfg.ResolvedClusters))
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	kc := writeKubeconfig(t, dir, "test.yaml", map[string]string{
		"minimal": "",
	})

	configContent := fmt.Sprintf(`
kubeconfigs:
  - %q
`, kc)

	cfgPath := writeTempConfig(t, configContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.ListenAddress != "127.0.0.1:9080" {
		t.Errorf("ListenAddress = %q, want %q", cfg.ListenAddress, "127.0.0.1:9080")
	}
}

func TestResolveMultipleContexts(t *testing.T) {
	dir := t.TempDir()
	kc := writeKubeconfig(t, dir, "multi.yaml", map[string]string{
		"cluster-a": "ns-a",
		"cluster-b": "ns-b",
	})

	cfg := &Config{
		ListenAddress: "127.0.0.1:9080",
		Kubeconfigs:   []string{kc},
	}

	if err := cfg.resolve(); err != nil {
		t.Fatalf("resolve() error: %v", err)
	}

	if len(cfg.ResolvedClusters) != 2 {
		t.Fatalf("len(ResolvedClusters) = %d, want 2", len(cfg.ResolvedClusters))
	}

	names := map[string]bool{}
	for _, rc := range cfg.ResolvedClusters {
		names[rc.Name] = true
	}

	if !names["cluster-a"] || !names["cluster-b"] {
		t.Errorf("expected cluster-a and cluster-b, got %v", names)
	}
}

func TestResolveDefaultNamespace(t *testing.T) {
	dir := t.TempDir()
	kc := writeKubeconfig(t, dir, "nons.yaml", map[string]string{
		"no-ns": "",
	})

	cfg := &Config{
		ListenAddress: "127.0.0.1:9080",
		Kubeconfigs:   []string{kc},
	}

	if err := cfg.resolve(); err != nil {
		t.Fatalf("resolve() error: %v", err)
	}

	if len(cfg.ResolvedClusters) != 1 {
		t.Fatalf("len(ResolvedClusters) = %d, want 1", len(cfg.ResolvedClusters))
	}

	if cfg.ResolvedClusters[0].Namespace != "default" {
		t.Errorf("Namespace = %q, want %q", cfg.ResolvedClusters[0].Namespace, "default")
	}
}

func TestValidateInvalidHTTPListenAddress(t *testing.T) {
	cfg := &Config{
		ListenAddress:     "127.0.0.1:9080",
		HTTPListenAddress: "not-a-valid-address",
		ResolvedClusters: []ResolvedCluster{
			{Name: "test", Kubeconfig: "/path"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should fail with invalid httpListenAddress")
	}
}

func TestLoadConfigWithHTTPListenAddress(t *testing.T) {
	dir := t.TempDir()
	kc := writeKubeconfig(t, dir, "test.yaml", map[string]string{
		"test-cluster": "default",
	})

	configContent := fmt.Sprintf(`
listenAddress: "127.0.0.1:9080"
httpListenAddress: "127.0.0.1:8080"
kubeconfigs:
  - %q
`, kc)

	cfgPath := writeTempConfig(t, configContent)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}

	if cfg.HTTPListenAddress != "127.0.0.1:8080" {
		t.Errorf("HTTPListenAddress = %q, want %q", cfg.HTTPListenAddress, "127.0.0.1:8080")
	}
}

func TestValidateInvalidListenAddress(t *testing.T) {
	cfg := &Config{
		ListenAddress: "not-a-valid-address",
		ResolvedClusters: []ResolvedCluster{
			{Name: "test", Kubeconfig: "/path"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should fail with invalid listenAddress")
	}
}

func TestValidateClusterNameWithDots(t *testing.T) {
	cfg := &Config{
		ListenAddress: "127.0.0.1:9080",
		ResolvedClusters: []ResolvedCluster{
			{Name: "my.cluster", Kubeconfig: "/path"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should fail with dots in cluster name")
	}
}

func TestValidateDuplicateNames(t *testing.T) {
	cfg := &Config{
		ListenAddress: "127.0.0.1:9080",
		ResolvedClusters: []ResolvedCluster{
			{Name: "dup", Kubeconfig: "/path1"},
			{Name: "dup", Kubeconfig: "/path2"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should fail with duplicate names")
	}
}

func TestValidateNoResolvedClusters(t *testing.T) {
	cfg := &Config{
		ListenAddress: "127.0.0.1:9080",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should fail with no resolved clusters")
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error: %v", err)
	}

	tests := []struct {
		input string
		want  string
	}{
		{"~/.kube/config", filepath.Join(home, ".kube", "config")},
		{"~/custom/path", filepath.Join(home, "custom", "path")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		got := expandTilde(tt.input)
		if got != tt.want {
			t.Errorf("expandTilde(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExpandGlobPattern(t *testing.T) {
	dir := t.TempDir()

	// create files to match
	for _, name := range []string{"a.yaml", "b.yaml", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0600); err != nil {
			t.Fatalf("creating file: %v", err)
		}
	}

	// glob with matches returns sorted paths
	matches, err := expandGlobPattern(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatalf("expandGlobPattern() error: %v", err)
	}

	if len(matches) != 2 {
		t.Fatalf("len(matches) = %d, want 2", len(matches))
	}

	if filepath.Base(matches[0]) != "a.yaml" || filepath.Base(matches[1]) != "b.yaml" {
		t.Errorf("matches = %v, want [a.yaml, b.yaml]", matches)
	}

	// no glob characters returns path unchanged
	literal := "/some/explicit/path.yaml"

	matches, err = expandGlobPattern(literal)
	if err != nil {
		t.Fatalf("expandGlobPattern() error: %v", err)
	}

	if len(matches) != 1 || matches[0] != literal {
		t.Errorf("expandGlobPattern(%q) = %v, want [%q]", literal, matches, literal)
	}

	// glob with no matches returns empty slice
	matches, err = expandGlobPattern(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("expandGlobPattern() error: %v", err)
	}

	if len(matches) != 0 {
		t.Errorf("len(matches) = %d, want 0", len(matches))
	}
}

func TestResolveGlobPattern(t *testing.T) {
	dir := t.TempDir()
	writeKubeconfig(t, dir, "alpha.yaml", map[string]string{"alpha": "ns-alpha"})
	writeKubeconfig(t, dir, "beta.yaml", map[string]string{"beta": "ns-beta"})

	cfg := &Config{
		ListenAddress: "127.0.0.1:9080",
		Kubeconfigs:   []string{filepath.Join(dir, "*.yaml")},
	}

	if err := cfg.resolve(); err != nil {
		t.Fatalf("resolve() error: %v", err)
	}

	if len(cfg.ResolvedClusters) != 2 {
		t.Fatalf("len(ResolvedClusters) = %d, want 2", len(cfg.ResolvedClusters))
	}

	names := map[string]bool{}
	for _, rc := range cfg.ResolvedClusters {
		names[rc.Name] = true
	}

	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected alpha and beta, got %v", names)
	}
}

func TestResolveGlobWithExplicitPath(t *testing.T) {
	dir := t.TempDir()

	// glob targets
	writeKubeconfig(t, dir, "glob1.yaml", map[string]string{"from-glob": "default"})

	// explicit file in a subdirectory
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	explicit := writeKubeconfig(t, subdir, "explicit.yaml", map[string]string{"from-explicit": "default"})

	cfg := &Config{
		ListenAddress: "127.0.0.1:9080",
		Kubeconfigs: []string{
			filepath.Join(dir, "*.yaml"),
			explicit,
		},
	}

	if err := cfg.resolve(); err != nil {
		t.Fatalf("resolve() error: %v", err)
	}

	if len(cfg.ResolvedClusters) != 2 {
		t.Fatalf("len(ResolvedClusters) = %d, want 2", len(cfg.ResolvedClusters))
	}

	names := map[string]bool{}
	for _, rc := range cfg.ResolvedClusters {
		names[rc.Name] = true
	}

	if !names["from-glob"] || !names["from-explicit"] {
		t.Errorf("expected from-glob and from-explicit, got %v", names)
	}
}

func TestResolveGlobNoMatches(t *testing.T) {
	dir := t.TempDir()

	cfg := &Config{
		ListenAddress: "127.0.0.1:9080",
		Kubeconfigs:   []string{filepath.Join(dir, "*.yaml")},
	}

	if err := cfg.resolve(); err != nil {
		t.Fatalf("resolve() should not error on zero matches, got: %v", err)
	}

	if len(cfg.ResolvedClusters) != 0 {
		t.Errorf("len(ResolvedClusters) = %d, want 0", len(cfg.ResolvedClusters))
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}

	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("closing temp file: %v", err)
	}

	return f.Name()
}

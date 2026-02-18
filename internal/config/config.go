package config

import (
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/tools/clientcmd"
)

//go:embed defaults.yaml
var DefaultConfigData []byte

// LogConfig holds logging configuration.
type LogConfig struct {
	Level     string `yaml:"level"`
	File      string `yaml:"file"`
	Formatter string `yaml:"formatter"`
	Colors    bool   `yaml:"colors"`
	Timestamp bool   `yaml:"timestamp"`
}

// Config holds the top-level application configuration.
type Config struct {
	ListenAddress         string    `yaml:"listenAddress"`
	HTTPListenAddress     string    `yaml:"httpListenAddress"`
	PACListenAddress      string    `yaml:"pacListenAddress"`
	SkipDefaultKubeconfig bool      `yaml:"skipDefaultKubeconfig"`
	SkipKubeconfigEnv     bool      `yaml:"skipKubeconfigEnv"`
	Kubeconfigs           []string  `yaml:"kubeconfigs"`
	Log                   LogConfig `yaml:"log"`
}

// defaultKubeconfigPathFunc returns the path to the default kubeconfig file.
// overridden in tests to point at a temp file.
var defaultKubeconfigPathFunc = func() string {
	return expandTilde("~/.kube/config")
}

// ResolvedCluster holds per-cluster settings derived from kubeconfig contexts.
type ResolvedCluster struct {
	Name       string
	Kubeconfig string
	Context    string
	Namespace  string
}

// LoadConfig reads a YAML config file and returns a validated Config
// along with the resolved clusters derived from kubeconfig discovery.
func LoadConfig(path string) (*Config, []ResolvedCluster, error) {
	var cfg Config

	// apply embedded defaults first
	if err := yaml.Unmarshal(DefaultConfigData, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parsing default config: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("reading config file: %w", err)
	}

	if len(data) > 0 {
		// overlay user config on top of defaults
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, nil, fmt.Errorf("parsing config file: %w", err)
		}
	}

	// set up the global logger early so resolve output uses the configured logger
	if err := SetupGlobalLogger(&cfg); err != nil {
		return nil, nil, fmt.Errorf("setting up logger: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid config: %w", err)
	}

	clusters, err := resolveKubeconfigs(&cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving kubeconfigs: %w", err)
	}

	if err := ValidateClusters(clusters); err != nil {
		return nil, nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, clusters, nil
}

// Validate checks that the static config fields are well-formed.
func (c *Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.ListenAddress); err != nil {
		return fmt.Errorf("invalid listenAddress %q: %w", c.ListenAddress, err)
	}

	if c.HTTPListenAddress != "" {
		if _, _, err := net.SplitHostPort(c.HTTPListenAddress); err != nil {
			return fmt.Errorf("invalid httpListenAddress %q: %w", c.HTTPListenAddress, err)
		}
	}

	if c.PACListenAddress != "" {
		if _, _, err := net.SplitHostPort(c.PACListenAddress); err != nil {
			return fmt.Errorf("invalid pacListenAddress %q: %w", c.PACListenAddress, err)
		}
	}

	return nil
}

// ValidateClusters checks that the resolved clusters are well-formed.
func ValidateClusters(clusters []ResolvedCluster) error {
	if len(clusters) == 0 {
		return errors.New("at least one cluster is required")
	}

	names := make(map[string]bool)

	for _, rc := range clusters {
		if rc.Name == "" {
			return errors.New("cluster name must not be empty")
		}

		if strings.Contains(rc.Name, ".") {
			return fmt.Errorf("cluster name %q must not contain dots", rc.Name)
		}

		if names[rc.Name] {
			return fmt.Errorf("duplicate cluster name %q", rc.Name)
		}

		names[rc.Name] = true
	}

	return nil
}

// resolveKubeconfigs discovers kubeconfigs in three phases:
//  1. default kubeconfig (~/.kube/config) — unless SkipDefaultKubeconfig is set
//  2. KUBECONFIG environment variable — unless SkipKubeconfigEnv is set
//  3. explicit paths and globs from the Kubeconfigs config field
func resolveKubeconfigs(cfg *Config) ([]ResolvedCluster, error) {
	seen := make(map[string]bool) // tracks files already loaded for deduplication

	var clusters []ResolvedCluster

	// phase 1: default kubeconfig
	if cfg.SkipDefaultKubeconfig {
		slog.Info("skipping default kubeconfig")
	} else {
		defaultPath := defaultKubeconfigPathFunc()
		if _, err := os.Stat(defaultPath); err == nil {
			resolved, err := loadKubeconfigFile(defaultPath, "default", seen)
			if err != nil {
				return nil, err
			}

			clusters = append(clusters, resolved...)
		} else {
			slog.Info("default kubeconfig not found", "path", defaultPath)
		}
	}

	// phase 2: KUBECONFIG environment variable
	if cfg.SkipKubeconfigEnv {
		slog.Info("skipping KUBECONFIG environment variable")
	} else {
		kubeconfigEnv := os.Getenv("KUBECONFIG")
		if kubeconfigEnv == "" {
			slog.Info("KUBECONFIG environment variable is not set")
		} else {
			paths := strings.SplitSeq(kubeconfigEnv, string(os.PathListSeparator))
			for p := range paths {
				p = expandTilde(strings.TrimSpace(p))
				if p == "" {
					continue
				}

				resolved, err := loadKubeconfigFile(p, "KUBECONFIG env", seen)
				if err != nil {
					return nil, err
				}

				clusters = append(clusters, resolved...)
			}
		}
	}

	// phase 3: explicit paths and globs from config
	for _, pattern := range cfg.Kubeconfigs {
		pattern = expandTilde(pattern)
		isGlob := strings.ContainsAny(pattern, "*?[")

		paths, err := expandGlobPattern(pattern)
		if err != nil {
			return nil, err
		}

		source := "config"
		if isGlob {
			source = "config glob"
		}

		for _, path := range paths {
			resolved, err := loadKubeconfigFile(path, source, seen)
			if err != nil {
				return nil, err
			}

			clusters = append(clusters, resolved...)
		}
	}

	if len(clusters) == 0 {
		slog.Warn("no kubeconfig files matched any configured patterns")
	}

	return clusters, nil
}

// loadKubeconfigFile loads a single kubeconfig file and returns the resolved
// clusters from its contexts. Already-seen files are skipped entirely.
func loadKubeconfigFile(path, source string, seenFiles map[string]bool) ([]ResolvedCluster, error) {
	if seenFiles[path] {
		slog.Debug("skipping already loaded kubeconfig", "path", path, "source", source)
		return nil, nil
	}

	seenFiles[path] = true

	kubeCfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig %q: %w", path, err)
	}

	var (
		clusters     []ResolvedCluster
		contextNames []string
	)

	for name, ctx := range kubeCfg.Contexts {
		ns := ctx.Namespace
		if ns == "" {
			ns = "default"
		}

		clusters = append(clusters, ResolvedCluster{
			Name:       name,
			Kubeconfig: path,
			Context:    name,
			Namespace:  ns,
		})

		contextNames = append(contextNames, name)
	}

	sort.Strings(contextNames)
	slog.Info("found kubeconfig contexts", "source", source, "path", path, "contexts", contextNames)

	return clusters, nil
}

func expandGlobPattern(pattern string) ([]string, error) {
	if !strings.ContainsAny(pattern, "*?[") {
		return []string{pattern}, nil
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
	}

	sort.Strings(matches)

	return matches, nil
}

func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}

	// only expand "~" or "~/..." — don't handle "~user" syntax
	if len(path) > 1 && path[1] != '/' && path[1] != filepath.Separator {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}

	if path == "~" {
		return home
	}

	return filepath.Join(home, path[2:])
}

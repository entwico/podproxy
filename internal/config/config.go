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
	ListenAddress     string    `yaml:"listenAddress"`
	HTTPListenAddress string    `yaml:"httpListenAddress"`
	PACListenAddress  string    `yaml:"pacListenAddress"`
	Kubeconfigs       []string  `yaml:"kubeconfigs"`
	Log               LogConfig `yaml:"log"`

	// populated by resolve(), not stored in YAML
	ResolvedClusters []ResolvedCluster `yaml:"-"`
}

// ResolvedCluster holds per-cluster settings derived from kubeconfig contexts.
type ResolvedCluster struct {
	Name       string
	Kubeconfig string
	Context    string
	Namespace  string
}

// LoadConfig reads a YAML config file and returns a validated Config.
func LoadConfig(path string) (*Config, error) {
	var cfg Config

	// apply embedded defaults first
	if err := yaml.Unmarshal(DefaultConfigData, &cfg); err != nil {
		return nil, fmt.Errorf("parsing default config: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	if len(data) > 0 {
		// overlay user config on top of defaults
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing config file: %w", err)
		}
	}

	// set up the global logger early so resolve() output uses the configured logger
	if err := SetupGlobalLogger(&cfg); err != nil {
		return nil, fmt.Errorf("setting up logger: %w", err)
	}

	if err := cfg.resolve(); err != nil {
		return nil, fmt.Errorf("resolving kubeconfigs: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// Validate checks that the config is well-formed.
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

	if len(c.ResolvedClusters) == 0 {
		return errors.New("at least one cluster is required")
	}

	names := make(map[string]bool)

	for _, rc := range c.ResolvedClusters {
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

// resolve parses each kubeconfig file and extracts all contexts into ResolvedClusters.
// Entries in Kubeconfigs can be explicit paths or glob patterns.
func (c *Config) resolve() error {
	for _, pattern := range c.Kubeconfigs {
		pattern = expandTilde(pattern)

		paths, err := expandGlobPattern(pattern)
		if err != nil {
			return err
		}

		for _, path := range paths {
			kubeCfg, err := clientcmd.LoadFromFile(path)
			if err != nil {
				return fmt.Errorf("loading kubeconfig %q: %w", path, err)
			}

			var contextNames []string

			for name, ctx := range kubeCfg.Contexts {
				ns := ctx.Namespace
				if ns == "" {
					ns = "default"
				}

				c.ResolvedClusters = append(c.ResolvedClusters, ResolvedCluster{
					Name:       name,
					Kubeconfig: path,
					Context:    name,
					Namespace:  ns,
				})

				contextNames = append(contextNames, name)
			}

			sort.Strings(contextNames)
			slog.Info("found kubeconfig contexts", "path", path, "contexts", contextNames)
		}
	}

	if len(c.ResolvedClusters) == 0 {
		slog.Warn("no kubeconfig files matched any configured patterns")
	}

	return nil
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

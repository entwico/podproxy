package nodeproxy

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed embed/proxy.mjs
var proxyScript []byte

// Install writes the bundled proxy.mjs to the given directory.
func Install(destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", destDir, err)
	}

	dest := filepath.Join(destDir, "proxy.mjs")

	if err := os.WriteFile(dest, proxyScript, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", dest, err)
	}

	return nil
}

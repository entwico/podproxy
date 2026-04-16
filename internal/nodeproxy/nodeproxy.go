package nodeproxy

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed embed/proxy.mjs
var proxyESM []byte

//go:embed embed/proxy.cjs
var proxyCJS []byte

// Install writes the bundled proxy.mjs and proxy.cjs to the given directory.
func Install(destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", destDir, err)
	}

	for name, data := range map[string][]byte{
		"proxy.mjs": proxyESM,
		"proxy.cjs": proxyCJS,
	} {
		dest := filepath.Join(destDir, name)

		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
	}

	return nil
}

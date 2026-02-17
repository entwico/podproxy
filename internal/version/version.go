package version

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
)

// Version is set at build time via ldflags.
var Version = "dev"

// Print outputs the application version and build information.
func Print() {
	fmt.Printf("podproxy version %s\n", Version)

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	fmt.Printf("go version: %s\n", info.GoVersion)

	settings := make(map[string]string, len(info.Settings))
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}

	fmt.Printf("build settings: %s\n", data)
}

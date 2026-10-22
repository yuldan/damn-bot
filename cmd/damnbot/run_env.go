package main

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
)

// loadRunEnv resolves the immutable config and the data, config and home
// directories for one headless run. It lives here (not in run.go) so run.go
// stays inside the E-14/E-71 file-length gate, and so runOne's cyclomatic
// complexity stays inside the E-51 gate.
func loadRunEnv(o *runOptions) (cfg config.Config, dataDir, configDir, home string, err error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return config.Config{}, "", "", "", fmt.Errorf("config deps: %w", err)
	}
	cfg, err = loadRunConfig(loader, o.configPath)
	if err != nil {
		return config.Config{}, "", "", "", err
	}
	if o.cfgOverride != nil {
		cfg = *o.cfgOverride
	}
	paths, err := loader.Paths("")
	if err != nil {
		return config.Config{}, "", "", "", fmt.Errorf("config paths: %w", err)
	}
	dataDir = o.dataDir
	if dataDir == "" {
		dataDir = paths.DataDir
	}
	return cfg, dataDir, paths.ConfigDir, homeDir(), nil
}

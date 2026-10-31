//go:build !minimal

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
)

// TestConfigGeneratedNoDrift (GAP-OPS-01): the checked-in reference is
// exactly what the tagged schema generates.
func TestConfigGeneratedNoDrift(t *testing.T) {
	want, err := generateConfigReference()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("..", "..", "docs", "CONFIG-REFERENCE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("docs/CONFIG-REFERENCE.md is stale; run go generate ./internal/config")
	}
}

// TestConfigExplainEnvSource (GAP-OPS-01): a generated override is reported
// as env, not as the file/default.
func TestConfigExplainEnvSource(t *testing.T) {
	loader, err := config.New(config.Deps{
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
		Getenv:   func(k string) string { return map[string]string{"DAMNBOT_LOOP_MAX_ITERATIONS": "7"}[k] },
		HomeDir:  func() (string, error) { return "/home/u", nil },
		GOOS:     "linux",
	})
	if err != nil {
		t.Fatal(err)
	}
	source, line := configOrigin(&configCLI{loader: loader}, "loop.max_iterations")
	if source != "env DAMNBOT_LOOP_MAX_ITERATIONS" || line != 0 {
		t.Fatalf("source = %q line %d", source, line)
	}
}

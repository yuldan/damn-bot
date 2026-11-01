//go:build minimal

package main

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
)

// TestMinimalBuildRefusesRemoteBackend: the minimal binary never links a
// remote adapter; selecting one fails closed.
func TestMinimalBuildRefusesRemoteBackend(t *testing.T) {
	cfg := config.Default()
	cfg.Exec.Backend = "docker"
	if _, _, err := newExecBackend(t.Context(), &cfg, exec.New(t.Context()), t.TempDir(), t.TempDir(), t.TempDir(), nil); !errors.Is(err, exec.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

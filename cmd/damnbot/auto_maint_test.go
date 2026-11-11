//go:build !minimal

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
)

// TestF8AUTO031AutoMaintenance proves the CLI-start hook mutates nothing with
// the default (disabled) config, removes only old <data>/tmp files when
// enabled, and keeps recent ones (AUTO-031).
func TestF8AUTO031AutoMaintenance(t *testing.T) {
	data := t.TempDir()
	tmp := filepath.Join(data, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(tmp, "old-session.jsonl")
	recentFile := filepath.Join(tmp, "recent-session.jsonl")
	for _, path := range []string{oldFile, recentFile} {
		if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(oldFile, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recentFile, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The zero/default config is off: zero files are removed, no error.
	cfg := &config.Config{}
	if err := startupMaintenance(context.Background(), cfg, data, func() time.Time { return now }); err != nil {
		t.Fatalf("default: %v", err)
	}
	if _, err := os.Stat(oldFile); err != nil {
		t.Fatalf("default config removed a file: %v", err)
	}

	cfg.Modules.Auto.Maintenance.Enabled = true
	if err := startupMaintenance(context.Background(), cfg, data, func() time.Time { return now }); err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("old file kept: %v", err)
	}
	if _, err := os.Stat(recentFile); err != nil {
		t.Fatalf("recent file removed: %v", err)
	}

	// An enabled run without a tmp directory is a clean no-op.
	if err := startupMaintenance(context.Background(), cfg, t.TempDir(), func() time.Time { return now }); err != nil {
		t.Fatalf("missing tmp: %v", err)
	}
}

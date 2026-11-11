//go:build !minimal

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/damnbot/damnbot/internal/auto"
	"github.com/damnbot/damnbot/internal/config"
)

// startupMaintenance is the AUTO-031 CLI-start hook: it runs the opt-in
// maintenance cleanup over <data>/tmp and keeps the newest 24 h. The default
// config returns before touching the filesystem (0 mutations without config).
func startupMaintenance(ctx context.Context, cfg *config.Config, dataDir string, now func() time.Time) error {
	if cfg == nil || !cfg.Modules.Auto.Maintenance.Enabled {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	if dataDir == "" {
		return errors.New("auto maintenance: empty data dir")
	}
	root := filepath.Join(dataDir, "tmp")
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("auto maintenance: %w", err)
	}
	svc, err := auto.New(autoConfig(cfg), nil, auto.WithNow(now))
	if err != nil {
		return fmt.Errorf("auto maintenance config: %w", err)
	}
	if _, err := svc.Maintain(ctx, auto.CleanupOptions{
		Root: root, Keep: 24 * time.Hour, Now: now(),
	}); err != nil {
		return fmt.Errorf("auto maintenance: %w", err)
	}
	return nil
}

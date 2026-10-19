//go:build !windows

package tools

import (
	"errors"
	"fmt"
	"os"
)

// syncDir makes a published blob durable (SPEC §11.6).
func syncDir(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // fixed data dir
	if err != nil {
		return fmt.Errorf("tools: open spill dir: %w", err)
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("tools: sync spill dir: %w", err)
	}
	return nil
}

//go:build windows

package journal

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// removeChild unlinks name from the pinned parent directory h. Windows has
// no unlinkat; the resolved parent path is reopened through os.Remove.
func removeChild(h *sandbox.Dir, name string) error {
	return os.Remove(filepath.Join(h.File().Name(), name))
}

// renameChild moves name in h to newName in dst.
func renameChild(h *sandbox.Dir, name string, dst *sandbox.Dir, newName string) error {
	return os.Rename(filepath.Join(h.File().Name(), name), filepath.Join(dst.File().Name(), newName))
}

// syncDirHandle is a no-op on Windows: directory handles cannot be flushed
// and NTFS journals directory metadata (ADR-094 butir 15).
func syncDirHandle(*sandbox.Dir) error { return nil }

func hardLinks(fs.FileInfo) uint64 { return 1 }

// makeReplaceable clears the read-only attribute before a temp file
// replaces the destination: NTFS refuses rename over a read-only file.
func makeReplaceable(ctx context.Context, sb *sandbox.Sandbox, resolved string) error {
	err := chmodFile(ctx, sb, resolved, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

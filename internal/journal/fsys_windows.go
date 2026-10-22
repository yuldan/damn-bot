//go:build windows

package journal

import (
	"context"
	"errors"
	"fmt"
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

// chmodFile applies mode to resolved, a path the sandbox checked. Windows
// cannot change the mode of a read-only file through an ordinary handle: the
// file refuses data access ("Access is denied") and
// SetFileInformationByHandle(FileBasicInfo) needs FILE_WRITE_ATTRIBUTES,
// which an O_RDONLY handle does not carry. os.Root.Chmod opens the entry
// with FILE_WRITE_ATTRIBUTES and FILE_FLAG_OPEN_REPARSE_POINT only, so a
// 0o440 destination still takes the mode and a link swapped in for the final
// component receives it instead of its target (JN2-1); os.Root also refuses
// every traversal that would leave root.
func chmodFile(_ context.Context, sb *sandbox.Sandbox, resolved string, mode fs.FileMode) error {
	root, rel, ok := sb.RootOf(resolved)
	if !ok || rel == "" || rel == "." {
		return fmt.Errorf("journal: mode %s: %w", resolved, sandbox.ErrInvalidPath)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("journal: mode %s: %w", resolved, err)
	}
	defer func() { _ = r.Close() }()
	if err := r.Chmod(rel, mode); err != nil {
		return fmt.Errorf("journal: mode %s: %w", resolved, err)
	}
	return nil
}

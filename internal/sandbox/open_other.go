//go:build !unix && !windows

package sandbox

import (
	"fmt"
	"io/fs"
	"os"
)

// openBeneath on OSes without openat (wasip1, js, plan9): os.Root, whose
// traversal never leaves dir. No fd-path verification exists there.
func (*Sandbox) openBeneath(dir, rel, _ string, flag int, perm fs.FileMode) (*os.File, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	defer closeDir(r)
	f, err := r.OpenFile(rel, flag, perm)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	return f, nil
}

// handlePath: no fd-path API on these OSes; recheckDeny relies on the
// identity check.
func handlePath(*os.File) (string, bool) { return "", false }

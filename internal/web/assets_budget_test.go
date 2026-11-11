//go:build !minimal

package web

import (
	"fmt"
	"io/fs"
	"testing"
)

// Asset budget of SPEC §13.2, repeated as a literal so raising MaxAssetBytes
// cannot silently loosen the acceptance criterion.
const f8AssetBudget = 1_500_000

// TestF8WebAssetsBudget is the F8 acceptance for the embedded asset set: the
// bytes the binary ships (dashboard assets plus the PWA files) stay under the
// 1.5 MB ceiling, and the check only exists in the full build — the minimal
// build carries no PWA assets and must not claim this budget.
func TestF8WebAssetsBudget(t *testing.T) {
	t.Parallel()
	if _, err := fs.Stat(pwaFiles, "pwassets/sw.js"); err != nil {
		t.Fatalf("minimal build must not run the web asset budget: %v", err)
	}
	if MaxAssetBytes > f8AssetBudget {
		t.Fatalf("MaxAssetBytes = %d > SPEC §13.2 ceiling %d", MaxAssetBytes, f8AssetBudget)
	}
	ui := treeBytes(t, embeddedAssets, ".")
	pwa := treeBytes(t, pwaFiles, ".")
	total := ui + pwa
	t.Logf("embedded web assets = %d B (ui %d B + pwa %d B), ceiling %d B", total, ui, pwa, f8AssetBudget)
	if ui == 0 || pwa == 0 {
		t.Fatalf("embedded asset trees must be non-empty (ui %d B, pwa %d B)", ui, pwa)
	}
	if total > f8AssetBudget {
		t.Fatalf("embedded web assets = %d B > %d B (SPEC §13.2)", total, f8AssetBudget)
	}
}

// treeBytes sums the regular-file sizes below root in fsys.
func treeBytes(t *testing.T, fsys fs.FS, root string) int64 {
	t.Helper()
	var n int64
	err := fs.WalkDir(fsys, root, func(name string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("stat %s: %w", name, ierr)
		}
		n += info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}

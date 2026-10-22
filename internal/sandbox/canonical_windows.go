//go:build windows

package sandbox

import (
	"fmt"
	"os"
)

// canonicalFinal expands an existing path to its final DOS spelling, so an
// 8.3 short root (RUNNER~1) becomes the long form every recorded path maps
// to (SEC14). Reparse points are expanded by resolve before this call.
func canonicalFinal(p string) (string, error) {
	f, err := os.Open(p) //nolint:gosec // G304: p is the sandbox root being canonicalized.
	if err != nil {
		return "", fmt.Errorf("sandbox: open root: %w", err)
	}
	got, ferr := finalPath(f)
	cerr := f.Close()
	if ferr != nil {
		return "", ferr
	}
	if cerr != nil {
		return "", cerr
	}
	return normalizeWindows(got), nil
}

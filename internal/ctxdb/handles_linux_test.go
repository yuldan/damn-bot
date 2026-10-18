//go:build linux

package ctxdb

import (
	"os"
	"path/filepath"
	"strings"
)

// openUnder lists this process's open descriptors that point inside dir
// (/proc/self/fd). Windows cannot delete or rename a file or directory that
// is still open, so every handle a Store, Restore or failed Open leaves
// behind is a real bug there; Linux CI catches the same leak here.
func openUnder(dir string) []string {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil
	}
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		p, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err == nil && (p == real || strings.HasPrefix(p, real+string(filepath.Separator))) {
			out = append(out, p)
		}
	}
	return out
}

//go:build unix

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyInode: the macOS/BSD post-open check accepts the path the fd
// was opened from and refuses a path naming another object or none.
func TestVerifyInode(t *testing.T) {
	t.Parallel()
	dir := realTemp(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	mkfile(t, a, "a")
	mkfile(t, b, "b")
	for _, tc := range []struct {
		want string
		ok   bool
	}{{a, true}, {b, false}, {filepath.Join(dir, "missing"), false}} {
		file, err := os.Open(a) //nolint:gosec // test-owned temp file.
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		got, err := verifyInode(file, tc.want)
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrOutsideWorkspace)) {
			t.Fatalf("verifyInode(%q) = %v", tc.want, err)
		}
		if got != nil {
			if err := got.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}
	}
}

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyProcFd(t *testing.T) {
	t.Parallel()
	dir := realTemp(t)
	p := filepath.Join(dir, "a")
	mkfile(t, p, "a")
	for _, tc := range []struct {
		want string
		ok   bool
	}{{p, true}, {filepath.Join(dir, "b"), false}} {
		file, err := os.Open(p) //nolint:gosec // test-owned temp file.
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		got, err := verifyProcFd(file, tc.want)
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrOutsideWorkspace)) {
			t.Fatalf("verifyProcFd(%q) = %v", tc.want, err)
		}
		if got != nil {
			if err := got.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}
	}
}

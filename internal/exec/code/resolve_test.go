//go:build unix

package code

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
)

// TestResolveCandidatesInChildPATH exercises the default resolver without any
// host interpreter: a stub executable in the child PATH stands in for python3,
// so the test is hermetic (Config.Lookup stays the production nil path).
func TestResolveCandidatesInChildPATH(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stub := filepath.Join(dir, "python3")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- fixture must be executable
		t.Fatal(err)
	}
	got, err := resolve(Python, []string{"PATH=" + dir}, "linux")
	if err != nil || len(got) != 3 || got[0] != stub || got[1] != "-I" || got[2] != "-B" {
		t.Fatalf("resolve = %v, %v", got, err)
	}
	missing, err := resolve(Python, []string{"PATH=" + t.TempDir()}, "linux")
	if !errors.Is(err, exec.ErrUnavailable) || missing != nil || !strings.Contains(err.Error(), "python3") {
		t.Fatalf("missing = %v, %v", missing, err)
	}
	if got, err := findInPath("/bin/sh", ""); err != nil || got != "/bin/sh" {
		t.Fatalf("separator path = %q, %v", got, err)
	}
}

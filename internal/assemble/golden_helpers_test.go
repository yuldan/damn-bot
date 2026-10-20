package assemble

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files") //nolint:gochecknoglobals // test-only golden flag

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // test-only golden path
	if err != nil {
		t.Fatalf("read golden %s (run go test -update): %v", path, err)
	}
	// A Windows checkout with core.autocrlf can still deliver CRLF despite the
	// -text gitattribute; compare canonical LF bytes on every host.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs (run go test -update and read the diff):\n got %d bytes, want %d bytes", path, len(got), len(want))
	}
}

//go:build !minimal

package doctor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// testLock builds a one-artifact lock; every field is validated by the
// production parser, so the test exercises the same fail-closed path.
func testLock(t *testing.T, payload []byte) *ToolsLock {
	t.Helper()
	doc := fmt.Sprintf(`{"schema":%q,"tools":[
	 {"name":"demo","version":"1.0","license":"MIT","license_url":"https://example.invalid/license",
	  "artifacts":[{"os":"linux","arch":"amd64","url":"https://example.invalid/demo-1.0",
	   "sha256":%q,"binary_sha256":%q,"size":%d,"binary":"demo"}]},
	 {"name":"unpinned","version":"","license":"MIT","license_url":"https://example.invalid/license",
	  "note":"no upstream binary","artifacts":[]}]}`,
		ToolsSchema, sha(payload), sha(payload), len(payload))
	lock, err := ParseToolsLock([]byte(doc))
	if err != nil {
		t.Fatalf("parse fixture lock: %v", err)
	}
	return lock
}

// installFixture writes payload at the lock's install path for demo 1.0.
func installFixture(t *testing.T, root string, payload []byte) string {
	t.Helper()
	path := filepath.Join(root, "tools", "demo", "1.0", "demo")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestToolsLock pins the offline half of GAP-TOOL-06: the embedded lock
// parses, every documented executable is listed, `ls` reports installation
// state without a network seam, and `verify` rejects a modified binary while
// never printing a URL.
func TestToolsLock(t *testing.T) {
	t.Parallel()
	t.Run("embedded lock", checkEmbeddedLock)
	t.Run("ls is offline", checkListOffline)
	t.Run("verify rejects a modified binary", checkVerifyTamper)
	t.Run("lock rejects path escape", checkLockEscape)
}

func checkEmbeddedLock(t *testing.T) {
	lock, err := EmbeddedToolsLock()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gh", "ffmpeg", "whisper.cpp", "piper", "chromium-headless-shell", "lightpanda"} {
		if _, ok := lock.Lookup(name); !ok {
			t.Errorf("lock lost %q", name)
		}
	}
	gh, _ := lock.Lookup("gh")
	a, ok := gh.Artifact("linux", "amd64")
	if !ok || a.Size <= 0 || len(a.SHA256) != 64 || len(a.BinarySHA256) != 64 {
		t.Fatalf("gh linux/amd64 artifact = %+v, %v", a, ok)
	}
	// Q8: the lock never installs a model, only executables.
	for _, p := range lock.Tools {
		if strings.Contains(strings.ToLower(p.Name), "model") {
			t.Errorf("lock names a model: %q", p.Name)
		}
	}
}

func checkListOffline(t *testing.T) {
	lock := testLock(t, []byte("demo-binary"))
	list := ToolsList(lock, t.TempDir(), "linux", "amd64")
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Installed || !list[0].Pinned || !list[0].OK || list[0].Detail != "not installed" {
		t.Fatalf("row = %+v", list[0])
	}
	if list[1].Pinned || list[1].Detail != "no pinned artifact for linux/amd64: no upstream binary" {
		t.Fatalf("unpinned row = %+v", list[1])
	}
}

func checkVerifyTamper(t *testing.T) {
	payload := []byte("demo-binary")
	lock := testLock(t, payload)
	root := t.TempDir()
	path := installFixture(t, root, payload)
	list, err := ToolsVerify(lock, root, "linux", "amd64")
	if err != nil || !list[0].OK || list[0].Detail != "verified" {
		t.Fatalf("clean verify = %+v, %v", list, err)
	}
	werr := os.WriteFile(path, []byte("demo-binarY"), 0o600)
	if werr != nil {
		t.Fatal(werr)
	}
	list, err = ToolsVerify(lock, root, "linux", "amd64")
	if !errors.Is(err, ErrToolVerify) || list[0].OK || !strings.Contains(list[0].Detail, "checksum mismatch") {
		t.Fatalf("tampered verify = %+v, %v", list, err)
	}
	for _, row := range list {
		if strings.Contains(row.Detail, "http") || strings.Contains(row.Path, "http") {
			t.Fatalf("verify printed a URL: %+v", row)
		}
	}
}

func checkLockEscape(t *testing.T) {
	doc := fmt.Sprintf(`{"schema":%q,"tools":[{"name":"demo","version":"1.0",`+
		`"license":"MIT","license_url":"https://example.invalid/l","artifacts":[{`+
		`"os":"linux","arch":"amd64","url":"https://example.invalid/x",`+
		`"sha256":%q,"binary_sha256":%q,"size":1,"binary":"../../evil"}]}]}`,
		ToolsSchema, sha([]byte("x")), sha([]byte("x")))
	if _, err := ParseToolsLock([]byte(doc)); !errors.Is(err, ErrToolLock) {
		t.Fatalf("parse = %v", err)
	}
}

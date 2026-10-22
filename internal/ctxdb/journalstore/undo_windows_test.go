//go:build windows

package journalstore

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/damnbot/damnbot/internal/journal"
)

// shortPath returns the 8.3 form of p, failing in CI when the volume has 8.3
// generation disabled (skipOrFail).
func shortPath(t *testing.T, p string) string {
	t.Helper()
	longp, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, serr := windows.GetShortPathName(longp, &buf[0], uint32(len(buf)))
		if serr != nil {
			skipOrFail(t, fmt.Sprintf("8.3 short name unavailable: %v", serr))
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n])
		}
		buf = make([]uint16, n+1)
	}
}

// longPath returns the long form of p (expands RUNNER~1 ancestors).
func longPath(t *testing.T, p string) string {
	t.Helper()
	shortp, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, serr := windows.GetLongPathName(shortp, &buf[0], uint32(len(buf)))
		if serr != nil {
			skipOrFail(t, fmt.Sprintf("long path unavailable: %v", serr))
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n])
		}
		buf = make([]uint16, n+1)
	}
}

// TestUndoRootShortNameForm: the workspace root may be configured with an 8.3
// short name (RUNNER~1) while recorded paths use the long form, or vice versa;
// both must undo (SEC14 root canonicalization).
func TestUndoRootShortNameForm(t *testing.T) {
	db, js, _ := openDB(t)
	longRoot := filepath.Join(longPath(t, t.TempDir()), "workspace-long-name")
	if err := os.MkdirAll(longRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	shortRoot := shortPath(t, longRoot)
	if strings.EqualFold(shortRoot, longRoot) {
		skipOrFail(t, "8.3 names are disabled on this volume")
	}
	for i, tc := range []struct{ name, cfgRoot, recordRoot string }{
		{"configured short, recorded long", shortRoot, longRoot},
		{"configured long, recorded short", longRoot, shortRoot},
	} {
		sid := fmt.Sprintf("r-short-%d", i)
		u := undoer(t, js, tc.cfgRoot)
		session(t.Context(), t, db, sid)
		p := filepath.Join(tc.recordRoot, fmt.Sprintf("f%d", i))
		put(t, p, []byte("new"), 0o644)
		res, err := u.Undo(t.Context(), sid, refusalWrite(t.Context(), t, js, sid, p, []byte("old"), []byte("new")), false)
		if err != nil || res.Status != journal.Undone || !bytes.Equal(content(t, p), []byte("old")) {
			t.Fatalf("%s: undo = %+v, %v", tc.name, res, err)
		}
	}
}

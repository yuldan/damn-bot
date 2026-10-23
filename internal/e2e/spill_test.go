package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/secret"
)

// spillConfigDir mirrors the binary's config directory for the harness's
// private home (SPEC §4.9); the key store of the encrypted run lives there.
func spillConfigDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "damnbot")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "damnbot")
	default:
		return filepath.Join(home, "damnbot")
	}
}

// sweep looks for needle in every regular file under dir. It reads through an
// os.Root so a symlink swapped in mid-walk cannot redirect the read (G122).
func sweep(t *testing.T, dir, needle string) (string, bool) {
	t.Helper()
	found := ""
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close %s: %v", dir, cerr)
		}
	}()
	if werr := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		raw, rerr := root.ReadFile(p)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", p, rerr)
		}
		if bytes.Contains(raw, []byte(needle)) {
			found = p
		}
		return nil
	}); werr != nil {
		t.Fatalf("sweep %s: %v", dir, werr)
	}
	return found, found != ""
}

// TestToolOutputSpillThroughBinary (SEC7-66/68, aturan owner): a tool output
// above InlineChars crosses the real binary, lands in the ctxdb blob store
// instead of the plaintext run-spill directory, is sealed when
// storage.encrypt is on, and produces a blob_fts row that recall serves. The
// negative control runs the same scenario unencrypted and proves the sweep
// does see the canary there.
//
//nolint:gocognit // one scenario per assertion: binary run, sweep, DB, recall
func TestToolOutputSpillThroughBinary(t *testing.T) {
	t.Parallel()
	const canary = "canary-spill-output"
	for _, tc := range []struct {
		name      string
		encrypted bool
		leaked    bool
	}{
		{"encrypted", true, false},
		{"negative-control", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := New(t,
				ToolCallReply("c1", "shell", `{"command":"cat big.txt"}`),
				providertest.Text("done"),
			)
			e.WriteFile("big.txt", strings.Repeat(canary+" ", 500)) // > InlineChars
			cfg := ""
			if tc.encrypted {
				requireUsableKeyStore(t)
				cfg = "[storage]\nencrypt = true\n"
			}
			if err := os.WriteFile(e.cfg, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			res := e.Run("read big", "--approval", "auto", "--unsafe-auto-shell")
			res.RequireOK(t)
			if got := len(e.Requests()); got != 2 {
				t.Fatalf("provider calls = %d, want 2", got)
			}
			if _, err := os.Stat(filepath.Join(e.DataDir, "run-spill")); err == nil {
				t.Fatalf("plaintext run-spill directory still exists")
			}
			if leaked := func() bool { _, ok := sweep(t, e.DataDir, canary); return ok }(); leaked != tc.leaked {
				t.Fatalf("canary in data dir = %v, want %v", leaked, tc.leaked)
			}
			if !tc.encrypted {
				return
			}
			// The encrypted DB carries the spilled tool output in blob_fts.
			codec, err := secret.OpenCodec(context.Background(), e.DataDir,
				mustKeyStore(t, e.home), false)
			if err != nil {
				t.Fatal(err)
			}
			db, err := ctxdb.Open(context.Background(), &ctxdb.Options{
				Dir: e.DataDir, Kind: "cli", ReadOnly: true, Codec: codec,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cerr := db.Close(); cerr != nil {
					t.Error(cerr)
				}
			}()
			n, cerr := db.Count(context.Background(), `SELECT count(*) FROM blob_fts f JOIN events e ON e.id = f.rowid
				WHERE e.kind = 'tool_result'`)
			if cerr != nil {
				t.Fatal(cerr)
			}
			if n == 0 {
				t.Fatalf("blob_fts tool_result rows = 0 (spill did not reach ctxdb)")
			}
		})
	}
}

func mustKeyStore(t *testing.T, home string) *secret.Store {
	t.Helper()
	st, err := secret.New(secret.Options{ConfigDir: spillConfigDir(home), BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/secret"
)

// requireUsableKeyStore skips scenarios that need the at-rest key store when
// the host keychain is present but unusable. macOS CI runners start the binary
// with a synthetic HOME, where keychain(1) cannot succeed; ADR-124 then fails
// closed with key_unavailable instead of falling back to a key file, so the
// encrypted scenarios cannot run there (Linux and Windows cover them).
func requireUsableKeyStore(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("keychain key store unavailable in CI; ADR-124 fails closed (key_unavailable)")
	}
}

// TestCryptoBackupRestore (acceptance F2 at-rest): the real binary runs with
// storage.encrypt=true, leaves no plaintext canary on disk, backs up, restores
// into a fresh data dir and the next run still sees the first turn.
func TestCryptoBackupRestore(t *testing.T) {
	t.Parallel()
	requireUsableKeyStore(t)
	const prompt = "prompt-canary-crypto"
	const reply = "reply-canary-crypto"
	e := New(t, providertest.Text(reply), providertest.Text("after restore"))
	enableEncryption(t, e)

	first := e.Run(prompt)
	first.RequireOK(t)
	var turn struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(first.Stdout), &turn); err != nil || turn.SessionID == "" {
		t.Fatalf("first run output = %q, %v", first.Stdout, err)
	}
	assertNoCanary(t, e.DataDir, prompt, reply)

	archive := e.Path("crypto.dmbk")
	e.Exec("sessions", "backup", "create", "--data-dir", e.DataDir, "--out", archive).RequireOK(t)
	assertNoCanary(t, archive, prompt, reply)

	fresh := filepath.Join(e.Workdir, "restored")
	e.Exec("sessions", "restore", "--data-dir", fresh, archive).RequireOK(t)
	assertNoCanary(t, fresh, prompt, reply)

	second := e.Run("continue after restore", "--data-dir", fresh)
	second.RequireOK(t)
	if got := len(e.Requests()); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	assertHistory(t, e, fresh, turn.SessionID, prompt)
}

func assertHistory(t *testing.T, e *Env, dataDir, sid, prompt string) {
	t.Helper()
	st, err := secret.New(secret.Options{ConfigDir: filepath.Join(e.home, "damnbot"), BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := secret.OpenCodec(context.Background(), dataDir, st, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dataDir, Kind: "cli", ReadOnly: true, Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close restored db: %v", cerr)
		}
	}()
	evs, err := db.History(context.Background(), sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range evs {
		if evs[i].Content == prompt {
			return
		}
	}
	t.Fatalf("restored history for %s lost the first prompt: %+v", sid, evs)
}

func enableEncryption(t *testing.T, e *Env) {
	t.Helper()
	const cfg = "[storage]\nencrypt = true\n"
	if err := os.WriteFile(e.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.home, "damnbot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNoCanary(t *testing.T, root string, canaries ...string) {
	t.Helper()
	assertCanary(t, root, false, canaries...)
}

func assertCanaryVisible(t *testing.T, root string, canaries ...string) {
	t.Helper()
	assertCanary(t, root, true, canaries...)
}

func assertCanary(t *testing.T, root string, present bool, canaries ...string) {
	t.Helper()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	if info.IsDir() {
		found = scanTree(t, root, canaries, present)
	} else {
		raw, rerr := os.ReadFile(filepath.Clean(root))
		if rerr != nil {
			t.Fatal(rerr)
		}
		found = scanCanaries(t, raw, root, canaries, present)
	}
	if present && !found {
		t.Fatalf("canary %v absent from %s", canaries, root)
	}
}

func scanTree(t *testing.T, root string, canaries []string, present bool) bool {
	t.Helper()
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := rootFS.Close(); cerr != nil {
			t.Errorf("close %s: %v", root, cerr)
		}
	}()
	found := false
	if werr := filepath.WalkDir(root, func(p string, d os.DirEntry, werr error) error {
		hit, eerr := scanPath(rootFS, root, p, d, werr, t, canaries, present)
		found = found || hit
		return eerr
	}); werr != nil {
		t.Fatal(werr)
	}
	return found
}

// TestCryptoPlaintextCanaryVisible is the negative control: without
// storage.encrypt the same sweep finds the canary.
func TestCryptoPlaintextCanaryVisible(t *testing.T) {
	t.Parallel()
	const reply = "plaintext-canary-reply"
	e := New(t, providertest.Text(reply))
	e.Run("plaintext canary prompt").RequireOK(t)
	assertCanaryVisible(t, e.DataDir, reply)
}

func scanPath(root *os.Root, base, p string, d os.DirEntry, werr error, t *testing.T, canaries []string, present bool) (bool, error) {
	t.Helper()
	if werr != nil || d.IsDir() {
		return false, werr
	}
	rel, rerr := filepath.Rel(base, p)
	if rerr != nil {
		return false, fmt.Errorf("relative %s: %w", p, rerr)
	}
	raw, rerr := root.ReadFile(rel)
	if rerr != nil {
		return false, fmt.Errorf("read %s: %w", rel, rerr)
	}
	return scanCanaries(t, raw, p, canaries, present), nil
}

func scanCanaries(t *testing.T, raw []byte, where string, canaries []string, present bool) bool {
	t.Helper()
	found := false
	for _, c := range canaries {
		if strings.Contains(string(raw), c) {
			found = true
			if !present {
				t.Fatalf("plaintext canary %q in %s", c, where)
			}
		}
	}
	return found
}

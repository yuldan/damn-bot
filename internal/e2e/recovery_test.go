//go:build unix

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestServeEarlyRecoveryRollback (OPS-028): a pending install whose target
// checksum does not match the binary restores .prev before the daemon starts.
func prepareRecovery(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	data := filepath.Join(dir, "data")
	for _, d := range []string{app, filepath.Join(data, "update")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(app, "damnbot")
	orig, rerr := os.ReadFile(binPaths.main)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if werr := os.WriteFile(bin, orig, 0o600); werr != nil { // #nosec G703
		t.Fatal(werr)
	}
	if cerr := os.Chmod(bin, 0o700); cerr != nil { // #nosec G302
		t.Fatal(cerr)
	}
	if werr := os.WriteFile(bin+".prev", []byte("prev-binary"), 0o600); werr != nil { // #nosec G703
		t.Fatal(werr)
	}
	sum := sha256.Sum256([]byte("expected-target"))
	p := map[string]string{
		"prev_binary": bin + ".prev", "prev_version": "1.0.0", "target_version": "1.1.0",
		"target_sha256": hex.EncodeToString(sum[:]), "created_at": time.Now().UTC().Format(time.RFC3339),
	}
	b, merr := json.Marshal(p)
	if merr != nil {
		t.Fatal(merr)
	}
	if werr := os.WriteFile(filepath.Join(data, "update", "pending.json"), b, 0o600); werr != nil { // #nosec G703
		t.Fatal(werr)
	}
	return bin, data
}

// TestServeEarlyRecoveryRollback (OPS-028): a pending install whose target
// checksum does not match the binary restores .prev before the daemon starts.
func TestServeEarlyRecoveryRollback(t *testing.T) {
	e := New(t)
	bin, data := prepareRecovery(t)
	cmd := exec.CommandContext(t.Context(), bin, "serve", "--data-dir", data, "--config", e.cfg, "--bind", "127.0.0.1:0") // #nosec G204 -- temp copy of this test's binary
	cmd.Dir = e.Workdir
	cmd.Env = e.childEnv()
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if serr := cmd.Start(); serr != nil {
		t.Fatal(serr)
	}
	waitLog(t, &stderr, "early recovery: rolled back")
	if serr := cmd.Process.Signal(syscall.SIGTERM); serr != nil {
		t.Logf("sigterm: %v", serr)
	}
	if werr := cmd.Wait(); werr != nil {
		t.Logf("wait: %v", werr)
	}
	if !strings.Contains(stderr.String(), "early recovery: rolled back") {
		t.Fatalf("no rollback log:\n%s", stderr.String())
	}
	got, err := os.ReadFile(bin) // #nosec G304
	if err != nil || string(got) != "prev-binary" {
		t.Fatalf("binary = %q err=%v, want prev", got, err)
	}
	if _, serr := os.Stat(filepath.Join(data, "update", "pending.json")); !os.IsNotExist(serr) {
		t.Fatal("pending kept after rollback")
	}
}

func waitLog(t *testing.T, stderr *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(stderr.String(), want) && time.Now().Before(deadline) {
		<-time.After(20 * time.Millisecond)
	}
}

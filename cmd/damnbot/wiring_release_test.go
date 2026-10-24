package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/redact"
)

// TestWiringFailureReleasesResources: a start that fails while building the
// tools (a broken skill goes through quarantine and fails closed) must release
// everything the wiring opened. Two assertions:
//
//   - no session lease survives the failed start, so a retry is not refused as
//     session_busy (deterministic on every OS);
//   - the data dir can be removed afterwards — the strong check on Windows,
//     where an open handle keeps the directory alive (that is exactly how
//     TestResumeReleasesLeaseOnStartFailure failed its TempDir cleanup before
//     this fix); on Unix removal succeeds with an open handle, so there it is
//     only a sanity check. Neither half is timing dependent.
func TestWiringFailureReleasesResources(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	cfgDir := filepath.Join(dir, "config")
	bad := filepath.Join(cfgDir, "skills", "bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("not a skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := providertest.New(t, nil, providertest.Text("unused"))
	cfg := config.Default()
	red := redact.New(redact.Options{})
	o := &runOptions{model: "openai/gpt-fake", baseURL: srv.URL, approval: "auto", providerAttempts: 1}
	if _, err := startRunModel(ctx, &cfg, dataDir, o.model, o, red, cfgDir, dir, nil); err == nil {
		t.Fatal("start with a broken skill succeeded")
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "cli", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	leases, lerr := db.Count(ctx, "SELECT count(*) FROM session_leases", nil)
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if lerr != nil {
		t.Fatal(lerr)
	}
	if leases != 0 {
		t.Errorf("a failed start left %d session lease(s) behind", leases)
	}
	// The retry must work (nothing is held), and the tree must be removable.
	if rerr := os.RemoveAll(bad); rerr != nil {
		t.Fatal(rerr)
	}
	model, err := startRunModel(ctx, &cfg, dataDir, o.model, o, red, cfgDir, dir, nil)
	if err != nil {
		t.Fatalf("retry start: %v", err)
	}
	if cerr := model.close(ctx); cerr != nil {
		t.Fatal(cerr)
	}
	if rerr := os.RemoveAll(dataDir); rerr != nil {
		t.Errorf("data dir is still held open after a failed start: %v", rerr)
	}
}

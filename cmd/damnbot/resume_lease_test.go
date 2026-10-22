package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/redact"
)

// TestResumeReleasesLeaseOnStartFailure (ACC-31): a resumed session that
// fails after AcquireLease must not keep session_busy, or the user cannot
// retry after fixing the cause.
func TestResumeReleasesLeaseOnStartFailure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	sid, _, err := newRunSession(ctx, db, "openai/gpt-fake")
	if err != nil {
		t.Fatal(err)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
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
	o := &runOptions{session: sid, model: "openai/gpt-fake", baseURL: srv.URL, approval: "auto", providerAttempts: 1}
	spill := diskSpill{dir: filepath.Join(dir, "spill")}
	if _, serr := startRunModel(ctx, &cfg, dataDir, o.model, o, red, spill, cfgDir, dir); serr == nil {
		t.Fatal("start with a broken skill succeeded")
	}
	if rerr := os.RemoveAll(bad); rerr != nil {
		t.Fatal(rerr)
	}
	model, merr := startRunModel(ctx, &cfg, dataDir, o.model, o, red, spill, cfgDir, dir)
	if merr != nil {
		if strings.Contains(merr.Error(), "session_busy") {
			t.Fatalf("lease not released after failed start: %v", merr)
		}
		t.Fatalf("retry start: %v", merr)
	}
	if cerr := model.close(ctx); cerr != nil {
		t.Fatal(cerr)
	}
}

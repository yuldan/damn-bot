//go:build !minimal

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/provider"
)

type autoFakeProvider struct {
	mu       sync.Mutex
	calls    int
	purposes []provider.Purpose
	content  string
	err      error
	called   chan struct{}
}

func (f *autoFakeProvider) Stream(context.Context, *loop.Request, func(loop.Delta)) (loop.Reply, error) {
	return loop.Reply{}, nil // purposeStream uses do on this fake
}

func (f *autoFakeProvider) do(_ context.Context, p provider.Purpose, _ *loop.Request, _ func(loop.Delta)) (loop.Reply, error) {
	f.mu.Lock()
	f.calls++
	f.purposes = append(f.purposes, p)
	if f.called != nil {
		select {
		case <-f.called:
		default:
			close(f.called)
		}
	}
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return loop.Reply{}, err
	}
	return loop.Reply{Content: f.content, Usage: event.Usage{Input: 2, Output: 2}}, nil
}

func openAutoDB(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: t.TempDir(), Kind: "serve"})
	if err != nil {
		t.Fatalf("ctxdb.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.CloseContext(context.Background()); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	if _, err := db.CreateSession(context.Background(), ctxdb.Session{ID: "S1", Origin: "gateway", Cwd: "/"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return db
}

// TestAutoDefaultOffNoCalls exercises the real serve constructor, tick
// and title hook with the default config: no scheduler task, no provider call,
// and no file touched.
func TestAutoDefaultOffNoCalls(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Default()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a, err := newServeAuto(&cfg, dataDir, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newServeAuto: %v", err)
	}
	root := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "old.tmp")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := a.Tick(context.Background()); len(got) != 0 {
		t.Fatalf("default tick ran tasks: %v", got)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("default auto deleted a file")
	}

	fake := &autoFakeProvider{content: "Title"}
	db := openAutoDB(t)
	if merr := a.maybeTitle(context.Background(), fake, db, "S1", "hello", "fake/model"); merr != nil {
		t.Fatalf("maybeTitle: %v", merr)
	}
	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 0 {
		t.Fatalf("default title called the provider %d times", calls)
	}
}

func TestF8AUTO002TitleGeneration(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.Modules.Auto.Title = config.ModulesAutoFeature{Enabled: true, DailyTokens: 100}
	a, err := newServeAuto(&cfg, dataDir, nil)
	if err != nil {
		t.Fatalf("newServeAuto: %v", err)
	}
	fake := &autoFakeProvider{content: " Fix   the login "}
	db := openAutoDB(t)
	if merr := a.maybeTitle(context.Background(), fake, db, "S1", "hello", "fake/model"); merr != nil {
		t.Fatalf("maybeTitle: %v", merr)
	}
	fake.mu.Lock()
	calls, purposes := fake.calls, append([]provider.Purpose(nil), fake.purposes...)
	fake.mu.Unlock()
	if calls != 1 || len(purposes) != 1 || purposes[0] != provider.PurposeAuto {
		t.Fatalf("calls/purpose = %d/%v", calls, purposes)
	}
	title, ok, err := db.SessionLabel(context.Background(), "S1", ctxdb.LabelTitle)
	if err != nil || !ok || title != "Fix the login" {
		t.Fatalf("title = %q ok=%v err=%v", title, ok, err)
	}
	// The title hook is wired through runTitle as well; it must stay off the
	// second turn because the label already exists.
	fake2 := &autoFakeProvider{content: "again", called: make(chan struct{})}
	a.runTitle(context.Background(), fake2, db, "S1", "again", "fake/model")
	select {
	case <-fake2.called:
		t.Fatal("runTitle called the provider for a titled session")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestF8AUTO030DiskCleanupScheduler(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.Modules.Auto.DiskCleanup = config.ModulesAutoFeature{Enabled: true, DailyTokens: 100}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a, err := newServeAuto(&cfg, dataDir, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newServeAuto: %v", err)
	}
	root := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "old.tmp")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := a.Tick(context.Background()); len(got) != 0 {
		t.Fatalf("task ran before due: %v", got)
	}
	now = now.Add(25 * time.Hour)
	got := a.Tick(context.Background())
	if len(got) != 1 || got[0].Feature != "disk_cleanup" || got[0].Err != nil {
		t.Fatalf("due tick = %v", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("scheduled cleanup left the old file: %v", err)
	}
}

func TestF8AUTO031MaintenanceScheduler(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.Modules.Auto.Maintenance = config.ModulesAutoFeature{Enabled: true, DailyTokens: 100}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a, err := newServeAuto(&cfg, dataDir, func() time.Time { return now })
	if err != nil {
		t.Fatalf("newServeAuto: %v", err)
	}
	root := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "old.tmp")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	if got := a.Tick(context.Background()); len(got) != 1 || got[0].Err != nil {
		t.Fatalf("maintenance tick = %v", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("maintenance left the old file")
	}
}

func TestServeAutoRunCancels(t *testing.T) {
	cfg := config.Default()
	a, err := newServeAuto(&cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auto Run leaked after cancel")
	}
}

package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
)

// TestNoticeGateOnce: a key is claimed exactly once, across gate instances
// (process restarts) and concurrent callers; another key is independent.
func TestNoticeGateOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &config.Config{UI: config.UI{Notices: true}}
	g := noticeGateFor(cfg, dir)
	ok, err := g.Allow("tip:one")
	if err != nil || !ok {
		t.Fatalf("first Allow = %t/%v", ok, err)
	}
	if ok, err = g.Allow("tip:one"); err != nil || ok {
		t.Fatalf("second Allow = %t/%v, want false", ok, err)
	}
	restarted := noticeGateFor(cfg, dir)
	if ok, err = restarted.Allow("tip:one"); err != nil || ok {
		t.Fatalf("after restart Allow = %t/%v, want false", ok, err)
	}
	if ok, err = restarted.Allow("tip:two"); err != nil || !ok {
		t.Fatalf("other key Allow = %t/%v, want true", ok, err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 20 {
		wg.Go(func() {
			ok, err := restarted.Allow("tip:race")
			if err != nil {
				t.Errorf("Allow: %v", err)
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("concurrent claims = %d, want 1", allowed)
	}
}

// TestNoticeGateDisabled: [ui] notices=false returns no gate, so no file or
// directory is created.
func TestNoticeGateDisabled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if g := noticeGateFor(&config.Config{UI: config.UI{Notices: false}}, dir); g != nil {
		t.Fatalf("disabled notices returned %T", g)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("disabled notices touched the data dir: %v/%v", entries, err)
	}
}

// TestNoticeGateErrorsAreReported: an unwritable store returns an error (the
// TUI drops the notice, startup continues).
func TestNoticeGateErrorsAreReported(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "notices")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := noticeGateFor(&config.Config{UI: config.UI{Notices: true}}, dir)
	if _, err := g.Allow("tip:one"); err == nil {
		t.Fatal("Allow on an unwritable store returned nil error")
	}
	if ok, err := g.Allow(""); err == nil || ok {
		t.Fatalf("empty key = %t/%v, want an error", ok, err)
	}
}

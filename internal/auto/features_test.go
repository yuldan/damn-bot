//go:build !minimal

package auto

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider"
)

type fakeTitles struct {
	calls int
	id    string
	title string
	err   error
}

func (f *fakeTitles) SetTitle(_ context.Context, id, title string) error {
	f.calls++
	f.id, f.title = id, title
	return f.err
}

type fakeCompactor struct {
	calls int
	err   error
}

func (f *fakeCompactor) Compact(context.Context) error { f.calls++; return f.err }

func enabled(f Feature) Config {
	return Config{Model: "fake/model", Features: map[Feature]Limits{f: {Enabled: true, DailyTokens: 100}}}
}

func TestF8AUTO002Title(t *testing.T) {
	t.Parallel()
	store := &fakeTitles{}
	fake := &fakeClient{resp: provider.Response{Content: "  Fix   the login  ", Usage: budget.Usage{Input: 1, Output: 1}}}

	off := mustNew(t, Config{Model: "fake/model"}, fake)
	if _, err := GenerateTitle(context.Background(), off, store, "s1", "hello"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if fake.count() != 0 || store.calls != 0 {
		t.Fatalf("disabled made calls: provider=%d store=%d", fake.count(), store.calls)
	}

	on := mustNew(t, enabled(Title), fake)
	title, err := GenerateTitle(context.Background(), on, store, "s1", "hello")
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if title != "Fix the login" || store.id != "s1" || store.title != title {
		t.Fatalf("title/store = %q/%q/%q", title, store.id, store.title)
	}

	fake.resp = provider.Response{Content: "  ", Usage: budget.Usage{Input: 1, Output: 1}}
	if _, err := GenerateTitle(context.Background(), on, store, "s2", "hello"); !errors.Is(err, ErrEmptyOutput) {
		t.Fatalf("empty: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("empty output wrote the store")
	}
	if _, err := GenerateTitle(context.Background(), on, nil, "s3", "hello"); !errors.Is(err, ErrNoStore) {
		t.Fatalf("nil store: %v", err)
	}
}

func TestF8AUTO013Suggestions(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{
		Content: "```json\n[{\"title\":\"Daily triage\",\"prompt\":\"triage issues\"}]\n```",
		Usage:   budget.Usage{Input: 1, Output: 1},
	}}
	off := mustNew(t, Config{}, fake)
	if _, err := Suggest(context.Background(), off, "repo"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if fake.count() != 0 {
		t.Fatal("disabled feature called the provider")
	}
	on := mustNew(t, enabled(Suggestions), fake)
	got, err := Suggest(context.Background(), on, "repo")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Daily triage" || got[0].Prompt != "triage issues" {
		t.Fatalf("suggestions = %+v", got)
	}
	fake.resp = provider.Response{Content: "not json", Usage: budget.Usage{Input: 1, Output: 1}}
	if _, err := Suggest(context.Background(), on, "repo"); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestF8AUTO004IdleCompaction(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{}
	c := &fakeCompactor{}
	off := mustNew(t, Config{}, fake)
	if _, err := off.MaybeCompact(context.Background(), CompactStats{Idle: true}, c); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if c.calls != 0 {
		t.Fatal("disabled compaction ran")
	}
	on := mustNew(t, enabled(Compaction), fake)
	ran, err := on.MaybeCompact(context.Background(), CompactStats{Tokens: 10, Threshold: 100}, c)
	if err != nil || ran || c.calls != 0 {
		t.Fatalf("below threshold: ran=%v err=%v calls=%d", ran, err, c.calls)
	}
	ran, err = on.MaybeCompact(context.Background(), CompactStats{Tokens: 100, Threshold: 100}, c)
	if err != nil || !ran || c.calls != 1 {
		t.Fatalf("at threshold: ran=%v err=%v calls=%d", ran, err, c.calls)
	}
}

func TestF8AUTO030DiskCleanup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := filepath.Join(root, "old.tmp")
	recent := filepath.Join(root, "recent.tmp")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	opts := CleanupOptions{Root: root, Keep: time.Hour, Now: now}

	off := mustNew(t, Config{}, nil)
	if _, err := off.Cleanup(context.Background(), opts); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("disabled cleanup deleted a file")
	}

	on := mustNew(t, enabled(DiskCleanup), nil)
	dry := opts
	dry.DryRun = true
	rep, err := on.Cleanup(context.Background(), dry)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if rep.Scanned != 2 || rep.Removed != 0 {
		t.Fatalf("dry report = %+v", rep)
	}
	if _, statErr := os.Stat(old); statErr != nil {
		t.Fatal("dry run deleted a file")
	}

	rep, err = on.Cleanup(context.Background(), opts)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if rep.Removed != 1 {
		t.Fatalf("removed = %d, want 1", rep.Removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file still present: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent file removed: %v", err)
	}
}

func TestF8AUTO031Maintenance(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := filepath.Join(root, "old.tmp")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	opts := CleanupOptions{Root: root, Keep: time.Hour, Now: now}
	off := mustNew(t, Config{}, nil)
	if _, err := off.Maintain(context.Background(), opts); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("disabled maintenance deleted a file")
	}
	on := mustNew(t, enabled(Maintenance), nil)
	if _, err := on.Maintain(context.Background(), opts); err != nil {
		t.Fatalf("maintain: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("maintenance did not remove the old file: %v", err)
	}
	if _, err := on.Maintain(context.Background(), CleanupOptions{Root: ""}); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("empty root: %v", err)
	}
}

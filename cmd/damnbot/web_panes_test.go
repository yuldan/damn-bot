//go:build !minimal

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/web"
)

// TestWebPaneBackend covers the UI-017 pane backend without an HTTP server:
// the project source, preview confinement, incremental output, and close.
func TestWebPaneBackend(t *testing.T) {
	if _, err := newWebPanes(""); err == nil {
		t.Fatal("newWebPanes accepted an empty data dir")
	}
	dir, _ := seedPaneWorkspace(t)
	p := newPaneBackend(t, dir)
	ctx := context.Background()
	checkPaneProjects(ctx, t, p)
	checkPanePreview(ctx, t, p, dir)
	if err := p.CloseAll(ctx); err != nil {
		t.Fatalf("close all: %v", err)
	}
}

// seedPaneWorkspace writes one active project and one archived project.
func seedPaneWorkspace(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "work", "demo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := f8ProjectState{Current: "demo", Items: []f8Project{
		{Name: "demo", Primary: root, Folders: []string{root}},
		{Name: "old", Primary: filepath.Join(dir, "work", "old"), Archived: true},
	}}
	if err := f8Save(f8StorePath(dir, "projects.json"), &st); err != nil {
		t.Fatal(err)
	}
	return dir, root
}

func newPaneBackend(t *testing.T, dir string) *webPanes {
	t.Helper()
	p, err := newWebPanes(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// checkPaneProjects proves archived projects are hidden and the current name
// is reported.
func checkPaneProjects(ctx context.Context, t *testing.T, p *webPanes) {
	t.Helper()
	projects, err := p.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Name != "demo" || !projects[0].Current {
		t.Fatalf("projects = %+v err=%v", projects, err)
	}
	if panes, listErr := p.List(ctx); listErr != nil || len(panes) != 0 {
		t.Fatalf("initial list = %+v err=%v", panes, listErr)
	}
}

// checkPanePreview opens, reads, closes, and refuses escapes.
func checkPanePreview(ctx context.Context, t *testing.T, p *webPanes, dir string) {
	t.Helper()
	pane, err := p.Open(ctx, web.PaneOpen{Kind: web.PanePreview, Target: "README.md", Project: "demo"})
	if err != nil || pane.Title != "README.md" || pane.Kind != web.PanePreview {
		t.Fatalf("open preview = %+v err=%v", pane, err)
	}
	chunk, err := p.Output(ctx, pane.ID, 0)
	if err != nil || string(chunk.Data) != "hello preview" || !chunk.Open {
		t.Fatalf("output = %+v err=%v", chunk, err)
	}
	if _, err := p.Output(ctx, pane.ID, len(chunk.Data)+1); err != nil {
		t.Fatalf("offset past end: %v", err)
	}
	if ierr := p.Input(ctx, pane.ID, []byte("x")); ierr == nil {
		t.Fatal("preview pane accepted terminal input")
	}
	if err := p.Close(ctx, pane.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := p.Close(ctx, pane.ID); !errors.Is(err, web.ErrPaneMissing) {
		t.Fatalf("double close = %v, want ErrPaneMissing", err)
	}
	for _, target := range []string{"", "../secret.txt", filepath.Join(dir, "outside.txt"), "."} {
		if _, err := p.Open(ctx, web.PaneOpen{Kind: web.PanePreview, Target: target, Project: "demo"}); err == nil {
			t.Fatalf("preview %q escaped the project", target)
		}
	}
	if _, err := p.Open(ctx, web.PaneOpen{Kind: web.PanePreview, Target: "README.md", Project: "missing"}); err == nil {
		t.Fatal("preview accepted an unknown project")
	}
}

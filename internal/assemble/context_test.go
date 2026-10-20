package assemble

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
)

func TestContextCaps(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	got, err := a.Context(context.Background(), fixtureContext())
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	c := asciiCounter()
	caps := []struct {
		name string
		text string
		cap  int
	}{
		{"facts", got.Slots.Facts, CapFacts},
		{"workspace", got.Slots.Workspace, CapWorkspace},
		{"files", got.Slots.Files, CapFiles},
		{"memory", got.Slots.Memory, CapMemory},
		{"skills", got.Slots.Skills, CapSkills},
		{"context", got.Text, CapContext},
	}
	for _, slot := range caps {
		if n := c.Count(slot.text); n > slot.cap {
			t.Errorf("%s = %d tokens, cap %d", slot.name, n, slot.cap)
		}
	}
	if got.Slots.Files == "" || got.Slots.Memory == "" || got.Slots.Skills == "" {
		t.Errorf("fixture did not fill every slot: %+v", got.Slots)
	}
}

// TestContextTruncationOrder: over the 1.100-token cap, AGENTS.md shrinks
// first (never below 200), then non-pinned memory, then skills (SPEC §6).
func TestContextTruncationOrder(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	in := &ContextInput{
		Facts:     fillBody("# Environment", "os=linux/amd64", CapFacts),
		Workspace: fillBody("# Workspace", "branch=main", CapWorkspace),
		Files:     []ContextFile{{Path: "AGENTS.md"}},
		Reader:    fixtureReader{"AGENTS.md": strings.Repeat("context file line. ", 5000)},
		Memory: []Memory{
			{Text: strings.Repeat("pinned memory. ", 900), Pinned: true},
			{Text: strings.Repeat("ranked memory. ", 900)},
			{Text: strings.Repeat("ranked memory. ", 900)},
		},
		Skills: []string{strings.Repeat("skill-a ", 200), strings.Repeat("skill-b ", 200), strings.Repeat("skill-c ", 200)},
	}
	got, err := a.Context(context.Background(), in)
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	c := asciiCounter()
	if n := c.Count(got.Slots.Files); n < CapFilesMin || n > CapFiles {
		t.Errorf("files = %d tokens, want %d..%d after shrinking", n, CapFilesMin, CapFiles)
	}
	if n := c.Count(got.Text); n > CapContext {
		t.Errorf("context = %d tokens, cap %d", n, CapContext)
	}
	if strings.Contains(got.Text, "ranked memory") && strings.Contains(got.Text, "skill-a") {
		t.Error("non-pinned memory and skills both survived an over-cap context")
	}
}

func TestContextSandboxRefusalOmitsFile(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	r := &recordingReader{files: map[string]string{
		"AGENTS.md": "inside",
		"secret.md": "must not appear",
	}}
	r.errs = map[string]error{"secret.md": fmt.Errorf("sandbox: %w", ErrUnavailable)}
	got, err := a.Context(context.Background(), &ContextInput{
		Files:  []ContextFile{{Path: "AGENTS.md"}, {Path: "secret.md"}, {Path: "missing.md"}},
		Reader: r,
	})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if !strings.Contains(got.Text, "inside") {
		t.Errorf("AGENTS.md missing: %q", got.Text)
	}
	if strings.Contains(got.Text, "must not appear") || strings.Contains(got.Text, "secret.md") {
		t.Errorf("refused file leaked: %q", got.Text)
	}
}

func TestContextRequiresReader(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	_, err := a.Context(context.Background(), &ContextInput{Files: []ContextFile{{Path: "AGENTS.md"}}})
	if !errors.Is(err, ErrDeps) {
		t.Fatalf("err = %v, want ErrDeps", err)
	}
}

func TestContextRejectsEmptyPath(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	_, err := a.Context(context.Background(), &ContextInput{
		Files:  []ContextFile{{Path: ""}},
		Reader: fixtureReader{},
	})
	if !errors.Is(err, ErrContextPath) {
		t.Fatalf("err = %v, want ErrContextPath", err)
	}
}

// TestContextScrub: every caller text passes through the injected redactor.
func TestContextScrub(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, func(s string) string { return strings.ReplaceAll(s, "sk-live-secret", "[REDACTED:secret]") })
	in := &ContextInput{
		Facts:     "token sk-live-secret",
		Workspace: "branch sk-live-secret",
		Files:     []ContextFile{{Path: "AGENTS.md"}},
		Reader:    fixtureReader{"AGENTS.md": "sk-live-secret in file"},
		Memory:    []Memory{{Text: "sk-live-secret memory"}},
		Skills:    []string{"sk-live-secret skill"},
	}
	got, err := a.Context(context.Background(), in)
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if strings.Contains(got.Text, "sk-live-secret") {
		t.Errorf("secret leaked into context: %q", got.Text)
	}
	if !strings.Contains(got.Text, "[REDACTED:secret]") {
		t.Errorf("scrub not applied: %q", got.Text)
	}
}

type recordingReader struct {
	files map[string]string
	errs  map[string]error
	seen  []string
}

func (r *recordingReader) Open(_ context.Context, path string) (io.ReadCloser, error) {
	r.seen = append(r.seen, path)
	if err := r.errs[path]; err != nil {
		return nil, err
	}
	body, ok := r.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

// TestContextEmptyAndMultipleFiles covers the nil/empty fast path and the
// per-file loop (more than one context file, each with its own heading).
func TestContextEmptyAndMultipleFiles(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	if got, err := a.Context(context.Background(), nil); err != nil || got.Text != "" {
		t.Fatalf("nil context = %+v, %v", got, err)
	}
	if got, err := a.Context(context.Background(), &ContextInput{}); err != nil || got.Text != "" {
		t.Fatalf("empty context = %+v, %v", got, err)
	}
	got, err := a.Context(context.Background(), &ContextInput{
		Files:  []ContextFile{{Path: "A.md"}, {Path: "B.md"}},
		Reader: fixtureReader{"A.md": "alpha", "B.md": "beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "# A.md") || !strings.Contains(got.Text, "# B.md") ||
		!strings.Contains(got.Text, "alpha") || !strings.Contains(got.Text, "beta") {
		t.Fatalf("multi-file context = %q", got.Text)
	}
}

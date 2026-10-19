package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/sandbox"
)

// askerRec answers clarify questions.
type askerRec struct {
	mu        sync.Mutex
	answer    string
	questions []Question
}

func (a *askerRec) Ask(_ context.Context, q Question) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.questions = append(a.questions, q)
	return a.answer, nil
}

// workspace builds a sandboxed workspace and file tools for tests.
type workspace struct {
	t      *testing.T
	root   string
	out    string // sibling file outside the root
	sb     *sandbox.Sandbox
	engine *codetools.Engine
	tools  []Tool
}

func newWorkspace(t *testing.T, deny ...string) *workspace {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(out, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{root}, DenyPaths: deny, Home: base})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := codetools.New(&codetools.Config{Sandbox: sb, Roots: []string{root}, NewEOL: "lf", Home: base})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	return &workspace{t: t, root: root, out: out, sb: sb, engine: eng, tools: FileTools(eng, sb)}
}

func (w *workspace) write(rel, content string) {
	w.t.Helper()
	p := filepath.Join(w.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func (w *workspace) read(rel string) string {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(w.root, filepath.FromSlash(rel)))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b)
}

func (w *workspace) dispatcher(t *testing.T, mode Mode, appr Approver) (*Dispatcher, *journalRec, *memSpill) {
	t.Helper()
	j := &journalRec{}
	sp := newMemSpill()
	reg, err := NewRegistry(w.tools...)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDispatcher(&Config{
		Registry: reg, Journal: j, Spill: sp, Redactor: testRedactor(), Mode: mode, Approver: appr,
		WorkspaceTrusted: true, Cwd: w.root,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, j, sp
}

// contentTool returns a tool whose Run returns a fixed content string.
func newContentTool(name, content string) *stubTool {
	return &stubTool{
		name: name, schema: echoSchema(name), kind: KindOther, effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) {
			return Result{OK: true, Content: content}, nil
		},
	}
}

func repeated(n int) string { return strings.Repeat("x", n) }

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeMapper struct {
	root, focus string
	max         int
	out         string
	err         error
}

func (f *fakeMapper) RepoMap(_ context.Context, root, focus string, max int) (string, error) {
	f.root, f.focus, f.max = root, focus, max
	return f.out, f.err
}

func TestCodeIntelToolsReadOnly(t *testing.T) {
	t.Parallel()
	m := &fakeMapper{out: "a.go\n  func A:1\n"}
	list := NewCodeIntelTools(&CodeIntelConfig{Mapper: m, Roots: []string{t.TempDir()}, Cwd: "/"})
	if len(list) != 2 || list[0].Name() != "repo_map" || list[1].Name() != "code_intel" {
		t.Fatalf("tools = %v", list)
	}
	checkReadOnly(t, list)
	rm, ok := list[0].(*repoMapTool)
	if !ok {
		t.Fatal("first tool is not repo_map")
	}
	root := rm.cfg.Roots[0]
	res, err := list[0].Run(t.Context(), Env{Cwd: root}, json.RawMessage(`{"focus":"A","max_tokens":200}`))
	if err != nil || !res.OK || !strings.Contains(res.Content, "func A") || m.focus != "A" || m.max != 200 {
		t.Fatalf("repo_map = %+v err=%v mapper=%+v", res, err, m)
	}
	if res.UntrustedSource != "workspace" {
		t.Fatalf("untrusted source = %q", res.UntrustedSource)
	}
	out, err := list[1].Run(t.Context(), Env{Cwd: root}, json.RawMessage(`{"action":"languages"}`))
	if err != nil || !strings.Contains(out.Content, "go, c, cpp") {
		t.Fatalf("languages = %+v err=%v", out, err)
	}
}

func TestCodeIntelToolNegatives(t *testing.T) {
	t.Parallel()
	m := &fakeMapper{}
	list := NewCodeIntelTools(&CodeIntelConfig{Mapper: m, Roots: []string{t.TempDir()}, Cwd: "/"})
	if _, err := list[0].Run(t.Context(), Env{}, json.RawMessage(`{"path":"/etc"}`)); codeOf(err) != CodeOutsideWorkspace {
		t.Fatalf("outside = %v", err)
	}
	if _, err := list[0].Run(t.Context(), Env{}, json.RawMessage(`{"max_tokens":1000,"focus":"`+strings.Repeat("x", 201)+`"}`)); codeOf(err) != CodeInvalidArgs {
		t.Fatalf("focus = %v", err)
	}
	if _, err := list[1].Run(t.Context(), Env{}, json.RawMessage(`{"action":"rename"}`)); codeOf(err) != CodeInvalidArgs {
		t.Fatalf("action = %v", err)
	}
	m.err = errors.New("codeintel: unsupported language: \"javascript\"")
	rm, ok := list[0].(*repoMapTool)
	if !ok {
		t.Fatal("not repo_map")
	}
	rm.cfg.Mapper = m
	_, err := rm.Run(t.Context(), Env{Cwd: rm.cfg.Roots[0]}, json.RawMessage(`{}`))
	te, ok := errors.AsType[*ToolError](err)
	if !ok || te.Code != CodeExecFailed || !strings.Contains(te.Message, "unsupported language") {
		t.Fatalf("unsupported = %#v", err)
	}
}

func checkReadOnly(t *testing.T, list []Tool) {
	t.Helper()
	for _, tl := range list {
		if tl.Effect(nil) != EffectRead || tl.Kind() != KindRead {
			t.Fatalf("%s not read-only", tl.Name())
		}
		ins, ok := tl.(Inspector)
		if !ok {
			t.Fatalf("%s is not an Inspector", tl.Name())
		}
		d, err := ins.Inspect(t.Context(), Env{}, json.RawMessage(`{}`), PolicyContext{Mode: ModeAsk})
		if err != nil || d.Requirement != RequireAuto {
			t.Fatalf("%s inspect = %+v %v", tl.Name(), d, err)
		}
	}
}

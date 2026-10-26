package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/toolset"
)

// toolsetProvider scripts one shell call and then a final answer, and records
// every request so the test can inspect the declared tool list.
type toolsetProvider struct {
	mu       sync.Mutex
	calls    int
	requests []loop.Request
	command  string
}

func (p *toolsetProvider) Stream(_ context.Context, req *loop.Request, emit func(loop.Delta)) (loop.Reply, error) {
	p.mu.Lock()
	p.calls++
	p.requests = append(p.requests, *req)
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		return loop.Reply{
			FinishReason: loop.FinishToolCalls, Model: "fake/m",
			ToolCalls: []loop.ToolCall{{ID: "c1", Name: "shell", Arguments: fmt.Sprintf(`{"command":%q}`, p.command)}},
		}, nil
	}
	emit(loop.Delta{Text: "done"})
	return loop.Reply{Content: "done", FinishReason: loop.FinishStop, Model: "fake/m"}, nil
}

func toolNames(req *loop.Request) []string {
	out := make([]string, len(req.Tools))
	for i, t := range req.Tools {
		out[i] = t.Name
	}
	return out
}

func toolMessages(req *loop.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role == "tool" {
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// runToolset drives runOne with the injected provider and a toolsets config.
func runToolset(t *testing.T, ts toolset.Config, approval, command string) (*toolsetProvider, string, error) {
	t.Helper()
	prov := &toolsetProvider{command: command}
	cfg := config.Default()
	cfg.Toolsets = ts
	var out, errs bytes.Buffer
	code, err := runOne(context.Background(), &runOptions{
		prompt: "go", model: "openai/fake", dataDir: t.TempDir(), output: "json",
		approval: approval, prov: prov, cfgOverride: &cfg,
	}, &out, &errs)
	if err != nil {
		return prov, errs.String(), err
	}
	if code != 0 {
		t.Fatalf("runOne = %d, stderr %s", code, errs.String())
	}
	return prov, errs.String(), nil
}

// TestRunToolsetExcludesShell (TSET-038/039, CORE-004): a toolset without
// shell removes it from the declared payload; a shell call is refused by the
// toolset filter and never executes.
func TestRunToolsetExcludesShell(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "pwned")
	ts := toolset.Config{
		Active: []string{"review"},
		Custom: map[string]toolset.Definition{"review": {Tools: []string{"read_file", "grep"}}},
	}
	prov, stderr, err := runToolset(t, ts, "auto", "echo pwned > "+marker)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(prov.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(prov.requests))
	}
	names := toolNames(&prov.requests[0])
	for _, name := range names {
		if name != "read_file" && name != "grep" {
			t.Fatalf("declared tools = %v, want only read_file and grep", names)
		}
	}
	msgs := toolMessages(&prov.requests[1])
	if !strings.Contains(msgs, "not in the active toolset") {
		t.Fatalf("tool result = %q, want the toolset refusal", msgs)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("shell executed despite the toolset filter")
	}
	if strings.Contains(stderr, "not available") {
		t.Fatalf("unexpected unavailable-tool warning: %s", stderr)
	}
}

// TestRunToolsetKeepsApproval (CORE-004): a toolset can expose shell, but it
// cannot change its approval class: headless still denies, and auto does not
// auto-approve shell either.
func TestRunToolsetKeepsApproval(t *testing.T) {
	t.Parallel()
	ts := toolset.Config{
		Active: []string{"term"},
		Custom: map[string]toolset.Definition{"term": {Tools: []string{"shell"}}},
	}
	for _, approval := range []string{"", "auto"} {
		t.Run("approval="+approval, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "pwned")
			prov, _, err := runToolset(t, ts, approval, "echo pwned > "+marker)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if names := toolNames(&prov.requests[0]); len(names) != 1 || names[0] != "shell" {
				t.Fatalf("declared tools = %v, want [shell]", names)
			}
			msgs := toolMessages(&prov.requests[1])
			if !strings.Contains(msgs, `"code":"denied"`) {
				t.Fatalf("tool result = %q, want the dispatcher's denial", msgs)
			}
			if strings.Contains(msgs, "not in the active toolset") {
				t.Fatalf("toolset filter, not approval, refused: %q", msgs)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("shell executed without approval")
			}
		})
	}
}

// TestToolsetRefusesDeferredInvoke (ACC-15): a toolset cannot reach a
// deferred/module tool through tool_invoke; the adapter checks the inner name
// before the dispatcher unwraps it.
func TestToolsetRefusesDeferredInvoke(t *testing.T) {
	t.Parallel()
	adapter := &toolsetAdapter{allowed: map[string]bool{"read_file": true, "tool_invoke": true}}
	_, err := adapter.Prepare(context.Background(), loop.ToolCall{
		ID: "c1", Name: "tool_invoke", Arguments: `{"name":"mcp_issue","arguments":{}}`,
	})
	var te *loop.ToolError
	if !errors.As(err, &te) || te.Code != "unknown_tool" || !strings.Contains(te.Message, "mcp_issue") {
		t.Fatalf("Prepare = %v, want unknown_tool for mcp_issue", err)
	}
}

// TestRunToolsetUnavailableWarned: a built-in toolset naming tools the binary
// does not implement reports them once instead of dropping them silently. The
// fixture is the `web` toolset (web_search/web_fetch): `memory` is implemented
// by this PR, so pointing here at an available toolset would assert nothing.
func TestRunToolsetUnavailableWarned(t *testing.T) {
	t.Parallel()
	ts := toolset.Config{Active: []string{"web"}}
	prov, stderr, err := runToolset(t, ts, "auto", "true")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stderr, `tool "web_search" is not available`) {
		t.Fatalf("stderr = %q, want the unavailable warning", stderr)
	}
	if len(prov.requests[0].Tools) != 0 {
		t.Fatalf("declared tools = %v, want none", toolNames(&prov.requests[0]))
	}
	_ = fmt.Sprint(prov.requests[0].Tools) // keep the shape explicit
}

// TestWithDeferredMeta: tool_search/tool_invoke are infrastructure, not
// toolset members; when they are registered they stay exposed so a toolset
// selection cannot disconnect the deferred catalog (#174 wiring).
func TestWithDeferredMeta(t *testing.T) {
	t.Parallel()
	sel := toolset.Selection{Allowed: map[string]bool{"read_file": true}, Tools: []string{"read_file"}}
	withDeferredMeta(&sel, map[string]bool{"read_file": true, "tool_search": true, "tool_invoke": true})
	if !sel.Allowed["tool_search"] || !sel.Allowed["tool_invoke"] {
		t.Fatalf("meta-tools not kept: %v", sel.Allowed)
	}
	if names := sel.Tools; len(names) != 3 || names[0] != "read_file" || names[1] != "tool_invoke" || names[2] != "tool_search" {
		t.Fatalf("Tools = %v", names)
	}
	legacy := toolset.Selection{Tools: []string{"read_file"}}
	withDeferredMeta(&legacy, map[string]bool{"tool_search": true})
	if legacy.Allowed != nil {
		t.Fatalf("legacy selection gained a filter: %+v", legacy)
	}
}

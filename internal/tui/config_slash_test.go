package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func slashApp(t *testing.T, cfg *Config) *Model {
	t.Helper()
	m, _, _ := newTestApp(t)
	m.host.Config = &fakeConfig{cfg: *cfg}
	return m
}

func runSlashText(t *testing.T, m *Model, text string) {
	t.Helper()
	m.input.SetValue(text)
	if cmd := m.submit(); cmd != nil {
		m.Update(cmd())
	}
}

func lastNotice(m *Model) string {
	if len(m.blocks) == 0 {
		return ""
	}
	return m.blocks[len(m.blocks)-1].Text
}

// TestSlashTheme applies a real session theme and refuses unknown names with
// the valid list.
func TestSlashTheme(t *testing.T) {
	t.Parallel()
	m := slashApp(t, &Config{Model: "fake/model"})
	before := fmt.Sprint(m.styles.header.GetForeground())
	runSlashText(t, m, "/theme mono")
	if m.theme.Name != "mono" {
		t.Fatalf("theme = %q, want mono", m.theme.Name)
	}
	if after := fmt.Sprint(m.styles.header.GetForeground()); after == before {
		t.Fatalf("theme did not change the render styles: %s", after)
	}
	if !strings.Contains(lastNotice(m), "theme mono") {
		t.Fatalf("theme notice = %q", lastNotice(m))
	}

	runSlashText(t, m, "/theme solarized")
	notice := lastNotice(m)
	for _, name := range []string{"tern-dark", "tern-light", "mono", "high-contrast"} {
		if !strings.Contains(notice, name) {
			t.Fatalf("unknown-theme notice missing %q: %q", name, notice)
		}
	}
	if m.theme.Name != "mono" {
		t.Fatalf("unknown theme changed the active theme to %q", m.theme.Name)
	}

	runSlashText(t, m, "/theme")
	if !strings.Contains(lastNotice(m), "theme mono; valid:") {
		t.Fatalf("bare /theme = %q", lastNotice(m))
	}
}

// TestSlashModel lists configured refs and refuses switching in this runtime.
func TestSlashModel(t *testing.T) {
	t.Parallel()
	m := slashApp(t, &Config{Model: "openai/current", Models: []string{"openai/current", "openai/other"}})
	runSlashText(t, m, "/model")
	notice := lastNotice(m)
	if !strings.Contains(notice, "openai/current") || !strings.Contains(notice, "openai/other") {
		t.Fatalf("/model list = %q", notice)
	}
	runSlashText(t, m, "/model openai/other")
	if !strings.Contains(lastNotice(m), "model switch refused") || !strings.Contains(lastNotice(m), "bound when the run opens") {
		t.Fatalf("/model switch was not refused with a reason: %q", lastNotice(m))
	}
	if got := m.host.Config.Snapshot().Model; got != "openai/current" {
		t.Fatalf("refused switch changed the model to %q", got)
	}
}

// TestSlashBudget uses the caps from config and the live turn/session cost.
func TestSlashBudget(t *testing.T) {
	t.Parallel()
	m := slashApp(t, &Config{
		Model:  "fake/model",
		Budget: BudgetInfo{TurnUSD: 2, SessionUSD: 10, DailyUSD: 50},
	})
	cost := 0.04
	m.Update(StreamMsg{Update: Update{Kind: KindUsage, Usage: &Usage{Iterations: 3, CostUSD: &cost}, At: m.host.Clock.Now()}})
	runSlashText(t, m, "/budget")
	notice := lastNotice(m)
	for _, want := range []string{"turn $0.04 / $2.00", "session $0.04 / $10.00", "day — / $50.00", "iter 3"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("/budget missing %q: %q", want, notice)
		}
	}

	empty := slashApp(t, &Config{Model: "fake/model"})
	runSlashText(t, empty, "/budget")
	if !strings.Contains(lastNotice(empty), "turn — / —") {
		t.Fatalf("empty budget = %q", lastNotice(empty))
	}
}

// TestSlashModules reads the runtime registry status.
func TestSlashModules(t *testing.T) {
	t.Parallel()
	m := slashApp(t, &Config{Model: "fake/model"})
	rt, ok := m.host.Runtime.(*fakeRuntime)
	if !ok {
		t.Fatalf("runtime = %T", m.host.Runtime)
	}
	rt.modules = []ModuleInfo{
		{ID: "codeintel", Group: "code", Status: "ready"},
		{ID: "browser", Group: "web", Status: "stopped"},
	}
	runSlashText(t, m, "/modules")
	notice := lastNotice(m)
	for _, want := range []string{"modules (2)", "codeintel · code · ready", "browser · web · stopped"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("/modules missing %q: %q", want, notice)
		}
	}

	rt.err = errors.New("runtime down")
	runSlashText(t, m, "/modules")
	if !strings.Contains(lastNotice(m), "modules unavailable: runtime down") {
		t.Fatalf("module error hidden: %q", lastNotice(m))
	}
}

// TestSlashContext opens the context panel with real window/usage metrics.
func TestSlashContext(t *testing.T) {
	t.Parallel()
	m := slashApp(t, &Config{Model: "fake/model"})
	rt, ok := m.host.Runtime.(*fakeRuntime)
	if !ok {
		t.Fatalf("runtime = %T", m.host.Runtime)
	}
	rt.info = ContextInfo{Window: 131072, Source: "registry"}
	m.Update(StreamMsg{Update: Update{Kind: KindUsage, Usage: &Usage{
		Input: 1000, CachedInput: 780, Iterations: 7,
	}, At: m.host.Clock.Now()}})
	m.input.SetValue("/context")
	cmd := m.submit()
	if cmd == nil {
		t.Fatalf("/context returned no load command")
	}
	m.Update(cmd())
	if m.Route() != routeContext {
		t.Fatalf("route = %q, want context", m.Route())
	}
	view := m.View().Content
	for _, want := range []string{"window: 131072 tok", "input: 1000 · cached: 780 · cache: 78%", "iterations: 7", "fixed overhead: —"} {
		if !strings.Contains(view, want) {
			t.Fatalf("context view missing %q:\n%s", want, view)
		}
	}

	rt.err = errors.New("window unknown")
	runSlashText(t, m, "/context")
	if !strings.Contains(m.View().Content, "context unavailable: window unknown") {
		t.Fatalf("context error hidden")
	}
}

// TestSlashReloadRefused: an invalid config keeps the old snapshot and says so.
func TestSlashReloadRefused(t *testing.T) {
	t.Parallel()
	m := slashApp(t, &Config{Model: "fake/model"})
	rt, ok := m.host.Runtime.(*fakeRuntime)
	if !ok {
		t.Fatalf("runtime = %T", m.host.Runtime)
	}
	rt.reloadErr = errors.New("ui.theme: must be tern-dark")
	runSlashText(t, m, "/reload")
	notice := lastNotice(m)
	if !strings.Contains(notice, "reload refused") || !strings.Contains(notice, "old snapshot kept") {
		t.Fatalf("/reload = %q", notice)
	}
}

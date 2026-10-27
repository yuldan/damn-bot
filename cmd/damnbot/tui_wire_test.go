package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tui"
)

// TestTranslateUpdate pins the loop/event → tui translation, including usage
// and the closed stop vocabulary that the TUI only carries.
func TestTranslateUpdate(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	cost := 0.25
	src := event.Update{
		Kind: event.UpdateStop, Text: "done", ToolCallID: "c1", ToolName: "read_file",
		Status: "ok", Stop: event.StopEndTurn, Detail: event.DetailCompleted, Err: "e",
		Usage: &event.Usage{Input: 10, CachedInput: 4, Output: 5, CostUSD: &cost, Iterations: 2},
	}
	want := tui.Update{
		Kind: tui.KindStop, Text: "done", ToolCallID: "c1", ToolName: "read_file",
		Status: "ok", Err: "e", At: at,
		Stop: tui.StopReason(event.StopEndTurn), Detail: tui.StopDetail(event.DetailCompleted),
		Usage: &tui.Usage{Input: 10, CachedInput: 4, Output: 5, CostUSD: &cost, Iterations: 2},
	}
	if got := translateUpdate(&src, at); !reflect.DeepEqual(got, want) {
		t.Fatalf("translateUpdate = %+v, want %+v", got, want)
	}
}

// TestTranslateUpdateNoUsage keeps a text delta free of accounting.
func TestTranslateUpdateNoUsage(t *testing.T) {
	t.Parallel()
	if got := translateUpdate(&event.Update{Kind: event.UpdateTextDelta, Text: "x"}, time.Time{}); got.Usage != nil {
		t.Fatalf("translateUpdate invented usage")
	}
}

// TestTUIRunnerLazyHost: the host answers Clock and ConfigSource before any
// store exists, and no model is opened by construction (P12).
func TestTUIRunnerLazyHost(t *testing.T) {
	t.Parallel()
	r := newTUIRunner(&tuiDeps{
		cfg: &config.Config{UI: config.UI{Theme: "mono"}, Approval: config.Approval{PreviewTimeoutS: 90}}, modelRef: "openai/fake",
		opts: &runOptions{}, red: nil,
	})
	if got := r.Snapshot().Model; got != "openai/fake" {
		t.Fatalf("Snapshot model = %q", got)
	}
	if got := r.Snapshot().Theme; got != "mono" {
		t.Fatalf("Snapshot theme = %q, want mono", got)
	}
	if got := r.Snapshot().ApprovalTimeout; got != 90*time.Second {
		t.Fatalf("Snapshot approval timeout = %v, want 90s", got)
	}
	if got := r.Now(); got.IsZero() {
		t.Fatalf("Now is zero")
	}
	if r.model != nil {
		t.Fatalf("newTUIRunner opened the session eagerly")
	}
	if r.Active("s") {
		t.Fatalf("inactive session reports active")
	}
	r.Cancel("s") // no loop yet: a no-op, never a panic
	if err := r.Close(); err != nil {
		t.Fatalf("Close on an unopened runner: %v", err)
	}
}

// TestToolActionID proves the composition root resolves the journal action
// anchored on a tool call (the ↶ marker of SPEC §13.1).
func TestToolActionID(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatalf("ctxdb: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	const sid = "S"
	if _, serr := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "tui", Cwd: "/"}); serr != nil {
		t.Fatalf("session: %v", serr)
	}
	ids, err := db.Append(ctx, sid, nil, ctxdb.Event{Kind: "tool_dispatch", ToolCallID: "c1", ToolName: "write_file", Role: "assistant"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	var want int64
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`INSERT INTO actions(session_id, event_id, kind, reversibility, subject, inverse, bytes_held, created_at)
			VALUES (?1,?2,'file_write','reversible','/w','{}',0,1) RETURNING id`, []any{sid, ids[0]}, &want)
	}); err != nil {
		t.Fatalf("action: %v", err)
	}
	if got := toolActionID(ctx, db, sid, "c1"); got != want {
		t.Fatalf("toolActionID = %d, want %d", got, want)
	}
	if got := toolActionID(ctx, db, sid, "missing"); got != 0 {
		t.Fatalf("missing call = %d, want 0", got)
	}
}

// TestConfiguredModels lists only refs the config names, deduplicated.
func TestConfiguredModels(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Provider: config.Provider{Model: "p/m", Light: "p/l", Fallback: []string{"p/f", "p/m"}},
		Providers: config.Providers{Custom: []config.CustomProvider{
			{Name: "c", Models: []config.CustomModel{{ID: "x"}, {ID: "y"}}},
		}},
	}
	got := configuredModels(cfg)
	want := []string{"p/m", "p/l", "p/f", "c/x", "c/y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredModels = %q, want %q", got, want)
	}
}

// TestTUIRunnerRuntimeInfo exercises the real module registry and model window
// behind /modules and /context.
func TestTUIRunnerRuntimeInfo(t *testing.T) {
	cfg := config.Default()
	cfg.Provider.Model = "openai/fake"
	cfg.Budget = config.Budget{TurnUSD: 1, SessionUSD: 2, DailyUSD: 3}
	o := &runOptions{prov: &injectProvider{}, newSessionID: "S-runtime", sessionOrigin: "tui", approvalOrigin: "tui"}
	r := newTUIRunner(&tuiDeps{
		cfg: &cfg, dataDir: t.TempDir(), modelRef: "openai/fake", opts: o,
		red: redact.New(redact.Options{}), configDir: t.TempDir(), home: t.TempDir(),
	})
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ctx := t.Context()
	info, err := r.Context(ctx)
	if err != nil || info.Window <= 0 || info.Source == "" {
		t.Fatalf("Context = %+v, %v", info, err)
	}
	mods, err := r.Modules(ctx)
	if err != nil {
		t.Fatalf("Modules: %v", err)
	}
	for _, m := range mods {
		if m.ID == "" || m.Group == "" || m.Status == "" {
			t.Fatalf("module row = %+v", m)
		}
	}
	snap := r.Snapshot()
	if !reflect.DeepEqual(snap.Models, []string{"openai/fake"}) {
		t.Fatalf("snapshot models = %q", snap.Models)
	}
	if snap.Budget != (tui.BudgetInfo{TurnUSD: 1, SessionUSD: 2, DailyUSD: 3}) {
		t.Fatalf("snapshot budget = %+v", snap.Budget)
	}
}

package e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestMemoryToolEndToEnd: the memory tool writes through the real service,
// evidencing the agent write with its dispatch event (TOOL-070).
func TestMemoryToolEndToEnd(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "memory", `{"action":"add","target":"user","content":"likes tabs"}`),
		providertest.Text("done"),
	)
	res := e.Run("remember my indentation preference", "--approval", "auto-edit")
	res.RequireOK(t)
	if !e.Contains(t, 1, "stored memory") || !e.Contains(t, 1, "quarantined") {
		t.Errorf("memory tool result not sent to the model:\n%s", e.RequestText(t, 1))
	}
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	}()
	var n int64
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT count(*) FROM memory WHERE origin = 'agent' AND scope = 'user'", nil, &n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("agent memory rows = %d, want 1", n)
	}
	var status string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT status FROM v_memory_status WHERE scope = 'user' AND origin = 'agent'`, nil, &status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "scanned" {
		t.Errorf("memory status = %q, want scanned (quarantined)", status)
	}
}

// TestSessionSearchEndToEnd: the session_search tool finds an earlier turn
// through real FTS (TOOL-073).
func TestSessionSearchEndToEnd(t *testing.T) {
	t.Parallel()
	e := New(t,
		providertest.Text("first answer"),
		ToolCallReply("c1", "session_search", `{"query":"NEEDLE-ALPHA","k":3}`),
		providertest.Text("second answer"),
	)
	e.Run("we discuss NEEDLE-ALPHA now").RequireOK(t)
	second := e.Run("find the earlier note")
	second.RequireOK(t)
	body := e.RequestText(t, 2)
	for _, want := range []string{"NEEDLE-ALPHA", "matches:"} {
		if !strings.Contains(body, want) {
			t.Errorf("search tool result lacks %q:\n%s", want, body)
		}
	}
}

// TestSessionSearchScopedToProject: session_search is scoped to the caller's
// project cwd (SEC17-1). A transcript seeded in another workspace of the same
// data dir is never returned; the caller's own workspace still matches the
// same query shape (negative control that proves the tool and FTS are live).
func TestSessionSearchScopedToProject(t *testing.T) {
	t.Parallel()
	e := New(t,
		providertest.Text("noted"),
		ToolCallReply("c1", "session_search", `{"query":"SCOPEMARK-BRAVO-9911"}`),
		providertest.Text("no hits"),
		ToolCallReply("c2", "session_search", `{"query":"SCOPEMARK-ALPHA-2244"}`),
		providertest.Text("found"),
	)
	e.Run("remember SCOPEMARK-ALPHA-2244 for this project").RequireOK(t)
	seedForeignSession(t, e, e.Workdir+"-other", "SCOPEMARK-BRAVO-9911 from another project")
	e.Run("recall the other repository notes").RequireOK(t)
	if body := e.RequestText(t, 2); !strings.Contains(body, "no matching sessions") {
		t.Errorf("foreign project transcript reached session_search:\n%s", body)
	}
	e.Run("recall our own notes").RequireOK(t)
	if body := e.RequestText(t, 4); !strings.Contains(body, "matches:") {
		t.Errorf("own project transcript missing (control):\n%s", body)
	} else if !strings.Contains(body, `untrusted source=\"session\"`) {
		t.Errorf("transcript hits are not wrapped as untrusted:\n%s", body)
	}
}

// seedForeignSession appends one transcript in a different workspace of the
// scenario's data dir: a session_search by cwd must never see it (SEC17-1).
func seedForeignSession(t *testing.T, e *Env, cwd, content string) {
	t.Helper()
	withStore(t, e, func(tx *ctxdb.Tx) error {
		if _, err := tx.CreateSession(ctxdb.Session{ID: "OTHER", Origin: "headless", Cwd: cwd}); err != nil {
			return fmt.Errorf("create foreign session: %w", err)
		}
		if _, err := tx.AppendEvent("OTHER", &ctxdb.Event{Kind: "user_message", Role: "user", Content: content}); err != nil {
			return fmt.Errorf("append foreign turn: %w", err)
		}
		return nil
	})
}

// TestMemoryAgentRemoveFailsClosed: the memory tool only retires notes the
// agent wrote; a user-origin note stays approved and the agent's own
// retirement is audited as the system actor, never the user (SEC17-3). The
// control retires an approved agent-written note through the same path.
func TestMemoryAgentRemoveFailsClosed(t *testing.T) {
	t.Parallel()
	e := New(t)
	userNote := seedUserMemory(t, e, "user-owned note")
	agentNote := seedAgentMemory(t, e, "agent-owned note")
	e.Server.Push(
		ToolCallReply("c1", "memory", fmt.Sprintf(`{"action":"remove","target":"user","id":%d}`, agentNote)),
		providertest.Text("removed"),
		ToolCallReply("c2", "memory", fmt.Sprintf(`{"action":"remove","target":"user","id":%d}`, userNote)),
		providertest.Text("done"),
	)
	e.Run("drop the note you wrote", "--approval", "auto-edit").RequireOK(t)
	if got := memoryQuery(t, e, `SELECT status FROM v_memory_status WHERE id = ?1`, agentNote); got != "retired" {
		t.Errorf("agent-origin note status = %q, want retired (control)", got)
	}
	if got := memoryQuery(t, e, `SELECT actor FROM memory_decisions WHERE memory_id = ?1 ORDER BY id DESC LIMIT 1`, agentNote); got != "system" {
		t.Errorf("retire actor = %q, want system (never user)", got)
	}
	e.Run("drop the user note too", "--approval", "auto-edit").RequireOK(t)
	if got := memoryQuery(t, e, `SELECT status FROM v_memory_status WHERE id = ?1`, userNote); got != "approved" {
		t.Errorf("user-origin note status = %q, want approved (fail closed)", got)
	}
	if got := memoryQuery(t, e, `SELECT count(*) FROM memory_decisions WHERE memory_id = ?1`, userNote); got != "1" {
		t.Errorf("user-origin decisions = %s, want 1 (no retire recorded)", got)
	}
	if body := e.RequestText(t, 3); !strings.Contains(body, "memory action denied") {
		t.Errorf("agent remove of user memory was not denied to the model:\n%s", body)
	}
}

// withStore runs fn against the scenario's data dir and closes the store
// before the child process starts.
func withStore(t *testing.T, e *Env, fn func(tx *ctxdb.Tx) error) {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	derr := db.Write(t.Context(), fn)
	if cerr := db.Close(); cerr != nil {
		t.Errorf("close store: %v", cerr)
	}
	if derr != nil {
		t.Fatal(derr)
	}
}

// seedUserMemory stores an approved user-origin note: the shape a manual
// memory write has before an agent tries to retire it.
func seedUserMemory(t *testing.T, e *Env, content string) int64 {
	t.Helper()
	var id int64
	withStore(t, e, func(tx *ctxdb.Tx) error {
		if err := tx.Get(`INSERT INTO memory(kind, category, scope, content, origin, scan_result, created_at)
			VALUES ('memory','fact','user',?1,'user','{}',0) RETURNING id`, []any{content}, &id); err != nil {
			return fmt.Errorf("insert user memory: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO memory_decisions(memory_id, status, actor, reason, created_at)
			VALUES (?1,'approved','user','confirmed',0)`, id); err != nil {
			return fmt.Errorf("insert user decision: %w", err)
		}
		return nil
	})
	return id
}

// seedAgentMemory stores an approved agent-origin note, evidenced by its own
// dispatch event (the P10 provenance an agent write always carries).
func seedAgentMemory(t *testing.T, e *Env, content string) int64 {
	t.Helper()
	var id int64
	withStore(t, e, func(tx *ctxdb.Tx) error {
		if _, err := tx.CreateSession(ctxdb.Session{ID: "SEED", Origin: "headless", Cwd: "/seed"}); err != nil {
			return fmt.Errorf("create seed session: %w", err)
		}
		ev, err := tx.AppendEvent("SEED", &ctxdb.Event{Kind: "tool_dispatch", ToolName: "memory", ToolCallID: "seed", Payload: "{}"})
		if err != nil {
			return fmt.Errorf("append seed dispatch: %w", err)
		}
		if err := tx.Get(`INSERT INTO memory(kind, category, scope, content, origin, source_session, evidence_event_id, scan_result, created_at)
			VALUES ('memory','fact','user',?1,'agent','SEED',?2,'{}',0) RETURNING id`, []any{content, ev}, &id); err != nil {
			return fmt.Errorf("insert agent memory: %w", err)
		}
		if _, xerr := tx.Exec(`INSERT INTO memory_decisions(memory_id, status, actor, reason, created_at)
			VALUES (?1,'approved','user','confirmed',0)`, id); xerr != nil {
			return fmt.Errorf("insert agent decision: %w", xerr)
		}
		return nil
	})
	return id
}

// memoryQuery reads one single-value row from the scenario store.
func memoryQuery(t *testing.T, e *Env, query string, args ...any) string {
	t.Helper()
	var out string
	withStore(t, e, func(tx *ctxdb.Tx) error {
		return tx.Get(query, args, &out)
	})
	return out
}

package e2e

import (
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

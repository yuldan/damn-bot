package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/redact"
)

// TestFailedTurnPersisted is GAP-REL-01's acceptance test against the real
// ctxdb: a non-retryable provider 400 after a tool result keeps every
// user/tool row, records turn_end and the append-only context_exclude
// marker, and the next turn on the same session never sends the excluded
// range while the rows stay recallable.
func TestFailedTurnPersisted(t *testing.T) {
	t.Parallel()
	call := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"todo","arguments":"{\"items\":[{\"id\":\"1\",\"text\":\"poison\",\"status\":\"pending\"}]}"}}]}}]}`
	end := `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
	usage := `{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":10}}`
	srv := providertest.New(t, nil,
		providertest.Reply{Frames: []string{call, end, usage, "[DONE]"}}, // tool round
		providertest.Status(400, "invalid_request_error", ""),            // non-retryable
		providertest.Text("fresh answer"),                                // next turn
	)
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Default()
	red := redact.New(redact.Options{})
	spill := diskSpill{dir: filepath.Join(dir, "spill")}
	o := &runOptions{model: "openai/gpt-fake", baseURL: srv.URL, providerAttempts: 1, output: "json"}
	model, err := startRunModel(ctx, &cfg, dir, o.model, o, red, spill, dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := model.close(ctx); cerr != nil {
			t.Error(cerr)
		}
	}()

	if code := turnCode(t, &cfg, "poison turn", o, model, red, spill); code != 2 {
		t.Fatalf("failed turn: exit %d, want 2", code)
	}
	for _, c := range []struct{ name, q string }{
		{"user rows", `SELECT count(*) FROM events WHERE session_id = ? AND kind = 'user_message' AND content = 'poison turn'`},
		{"tool rows", `SELECT count(*) FROM events WHERE session_id = ? AND kind = 'tool_result' AND tool_call_id = 'c1'`},
		{"turn_end{provider_error}", `SELECT count(*) FROM events WHERE session_id = ? AND kind = 'turn_end'
			AND json_extract(payload, '$.detail') = 'provider_error'`},
		{"context_exclude", `SELECT count(*) FROM events WHERE session_id = ? AND kind = 'context_exclude'
			AND json_extract(payload, '$.reason') = 'provider_error'`},
	} {
		if n := rowCount(t, model, c.q); n != 1 {
			t.Fatalf("%s = %d, want 1", c.name, n)
		}
	}

	if code := turnCode(t, &cfg, "fresh turn", o, model, red, spill); code != 0 {
		t.Fatalf("next turn: exit %d, want 0", code)
	}
	reqs := srv.Requests()
	if len(reqs) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(reqs))
	}
	next := string(reqs[2])
	if strings.Contains(next, "poison turn") || strings.Contains(next, `"tool_call_id":"c1"`) {
		t.Fatalf("excluded range sent to the provider: %s", next)
	}
	if !strings.Contains(next, "fresh turn") {
		t.Fatalf("new user message missing: %s", next)
	}
	// Nothing was deleted: both user messages and the tool row remain.
	for _, c := range []struct {
		name string
		want int64
		q    string
	}{
		{"user rows after retry", 2, `SELECT count(*) FROM events WHERE session_id = ? AND kind = 'user_message'`},
		{"tool rows after retry", 1, `SELECT count(*) FROM events WHERE session_id = ? AND kind = 'tool_result'`},
	} {
		if n := rowCount(t, model, c.q); n != c.want {
			t.Fatalf("%s = %d, want %d", c.name, n, c.want)
		}
	}
	hits, herr := model.db.Search(ctx, ctxdb.Query{Text: "poison", SessionID: model.sessionID})
	if herr != nil || len(hits) == 0 {
		t.Fatalf("recall = %+v, %v", hits, herr)
	}
}

// turnCode runs one headless turn on the existing model/session.
func turnCode(t *testing.T, cfg *config.Config, prompt string, o *runOptions, model *runModel, red *redact.Redactor, spill diskSpill) int {
	t.Helper()
	opts := &runOptions{prompt: prompt, model: o.model, baseURL: o.baseURL, providerAttempts: 1, output: "json"}
	code, err := runTurn(t.Context(), cfg, opts, model, o.model, red, spill, io.Discard)
	if err != nil {
		t.Fatalf("turn %q: %v", prompt, err)
	}
	return code
}

// rowCount counts one session-scoped query (a single ? placeholder).
func rowCount(t *testing.T, model *runModel, q string) int64 {
	t.Helper()
	n, err := model.db.Count(t.Context(), q, model.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

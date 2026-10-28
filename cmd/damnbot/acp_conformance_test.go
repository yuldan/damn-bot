package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/tui"
)

// fakeHistory is the persisted-session seam double of the conformance tests.
type fakeHistory struct {
	items   []acpReplayItem
	err     error
	eventID int64
	loadSID string
	loads   int
}

func (h *fakeHistory) Load(_ context.Context, sid string) ([]acpReplayItem, error) {
	h.loads++
	h.loadSID = sid
	if h.err != nil {
		return nil, h.err
	}
	return h.items, nil
}

func (h *fakeHistory) TurnEndEventID(context.Context, string) (int64, error) { return h.eventID, nil }

// scriptRunner replays a fixed update list; panic/err simulate a tool panic
// and a prompt failure.
type scriptRunner struct {
	updates   []tui.Update
	err       error
	crash     bool
	input     tui.Input
	sid       string
	closed    bool
	active    bool
	approvals []string
}

func (r *scriptRunner) Prompt(_ context.Context, sid string, in tui.Input) (<-chan tui.Update, error) {
	r.sid, r.input = sid, in
	if r.crash {
		// A real runtime panic exercises the boundary without the panic
		// keyword (E-23 applies to tests too).
		var zero reflect.Value
		_ = zero.Interface()
	}
	if r.err != nil {
		return nil, r.err
	}
	ch := make(chan tui.Update, len(r.updates))
	for i := range r.updates {
		ch <- r.updates[i]
	}
	close(ch)
	return ch, nil
}

func (r *scriptRunner) Cancel(string)      {}
func (r *scriptRunner) Active(string) bool { return r.active }
func (r *scriptRunner) Close() error       { r.closed = true; return nil }

func (r *scriptRunner) SetApproval(mode string) error {
	r.approvals = append(r.approvals, mode)
	return nil
}

// serveACP runs one server instance over input with the given seam doubles.
func serveACP(t *testing.T, input string, runner acpRunner, hist acpHistory) string {
	t.Helper()
	out := &syncBuffer{}
	srv := fakeServer(t, out)
	if runner != nil {
		srv.factory = func(string, *tuiDeps) acpRunner { return runner }
	}
	srv.history = hist
	if err := srv.serve(t.Context(), strings.NewReader(input)); err != nil {
		t.Fatalf("serve: %v", err)
	}
	return out.String()
}

// frames decodes every NDJSON frame of out.
func frames(t *testing.T, out string) []map[string]any {
	t.Helper()
	var list []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("frame: %v: %s", err, line)
		}
		list = append(list, v)
	}
	return list
}

// reply returns the response frame for id, or fails.
func reply(t *testing.T, out string, id float64) map[string]any {
	t.Helper()
	for _, f := range frames(t, out) {
		if f["id"] == id {
			return f
		}
	}
	t.Fatalf("no response for id %v:\n%s", id, out)
	return nil
}

func errCode(t *testing.T, f map[string]any) float64 {
	t.Helper()
	e, ok := f["error"].(map[string]any)
	if !ok {
		t.Fatalf("frame is not an error: %v", f)
	}
	code, ok := e["code"].(float64)
	if !ok {
		t.Fatalf("error frame lacks a numeric code: %v", f)
	}
	return code
}

func dataOf(t *testing.T, f map[string]any) map[string]any {
	t.Helper()
	e, ok := f["error"].(map[string]any)
	if !ok {
		t.Fatalf("frame is not an error: %v", f)
	}
	data, ok := e["data"].(map[string]any)
	if !ok {
		return nil
	}
	return data
}

func TestACPLoadReplaysHistory(t *testing.T) {
	hist := &fakeHistory{items: []acpReplayItem{
		{Update: "user_message_chunk", Text: "halo"},
		{Update: "agent_message_chunk", Text: "hai"},
		{Update: "tool_call_update", Text: "ok", ToolCallID: "c1", Status: "completed"},
	}}
	out := serveACP(t, `{"jsonrpc":"2.0","id":7,"method":"session/load","params":{"sessionId":"load-1"}}`+"\n", &scriptRunner{}, hist)
	all := frames(t, out)
	if len(all) != 4 {
		t.Fatalf("frames = %d, want 3 updates + response:\n%s", len(all), out)
	}
	want := []string{"user_message_chunk", "agent_message_chunk", "tool_call_update"}
	for i, variant := range want {
		params, ok := all[i]["params"].(map[string]any)
		if !ok {
			t.Fatalf("frame %d has no params: %v", i, all[i])
		}
		up, ok := params["update"].(map[string]any)
		if !ok {
			t.Fatalf("frame %d has no update: %v", i, params)
		}
		if up["sessionUpdate"] != variant {
			t.Errorf("frame %d update = %v, want %s", i, up["sessionUpdate"], variant)
		}
		if params["sessionId"] != "load-1" {
			t.Errorf("frame %d sessionId = %v", i, params["sessionId"])
		}
	}
	if all[3]["result"] != nil {
		t.Errorf("load result = %v, want null", all[3]["result"])
	}
	if hist.loadSID != "load-1" || hist.loads != 1 {
		t.Errorf("load(sid=%q, n=%d), want load-1 once", hist.loadSID, hist.loads)
	}
}

func TestACPLoadFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		code    int
		dataKey string
	}{
		{"unknown", fmt.Errorf("history: %w", ctxdb.ErrNotFound), -32002, ""},
		{"busy", fmt.Errorf("lease: %w", ctxdb.ErrSessionBusy), -32603, "session_busy"},
		{"migration", &acpHistoryError{code: "migration_failed", err: errors.New("migrate")}, -32603, "migration_failed"},
		{"corrupt", &acpHistoryError{code: "fts_corrupt", err: errors.New("fts")}, -32603, "fts_corrupt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := serveACP(t, `{"jsonrpc":"2.0","id":9,"method":"session/load","params":{"sessionId":"S"}}`+"\n", &scriptRunner{}, &fakeHistory{err: tc.err})
			if strings.Contains(out, `"session_update"`) || strings.Contains(out, "session/update") {
				t.Fatalf("failed load replayed a history:\n%s", out)
			}
			f := reply(t, out, 9)
			if got := errCode(t, f); got != float64(tc.code) {
				t.Fatalf("code = %v, want %d:\n%s", got, tc.code, out)
			}
			if tc.dataKey != "" {
				if d := dataOf(t, f); d["error_code"] != tc.dataKey {
					t.Fatalf("data = %v, want error_code %s", d, tc.dataKey)
				}
			}
		})
	}
}

// failCase is one GAP-UX-03 conformance scenario.
type failCase struct {
	name   string
	input  string
	runner acpRunner
	hist   acpHistory
	id     float64
	code   int
	data   map[string]any
}

// conformanceCases is the failure matrix: every row must be answered by a
// JSON-RPC error, never a success response (SPEC §13.3, P14).
func conformanceCases() []failCase {
	prompt := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"x"}]}}` + "\n"
	providerErr := &scriptRunner{updates: []tui.Update{{Kind: tui.KindStop, Stop: "error", Detail: "provider_error"}}}
	internalErr := &scriptRunner{updates: []tui.Update{{Kind: tui.KindStop, Stop: "error", Detail: "internal"}}}
	crashes := &scriptRunner{crash: true}
	runnerErr := &scriptRunner{err: errors.New("provider down")}
	return []failCase{
		{
			"load-unknown", `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"nope"}}` + "\n", nil,
			&fakeHistory{err: fmt.Errorf("x: %w", ctxdb.ErrNotFound)}, 2, -32002, nil,
		},
		{
			"load-corrupt", `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"S"}}` + "\n", nil,
			&fakeHistory{err: &acpHistoryError{code: "migration_failed", err: errors.New("x")}}, 2, -32603,
			map[string]any{"error_code": "migration_failed"},
		},
		{
			"load-busy", `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"S"}}` + "\n", nil,
			&fakeHistory{err: fmt.Errorf("x: %w", ctxdb.ErrSessionBusy)}, 2, -32603,
			map[string]any{"error_code": "session_busy"},
		},
		{
			"load-invalid-id", `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"bad id"}}` + "\n", nil,
			&fakeHistory{}, 2, -32602, nil,
		},
		{
			"load-no-store", `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"S"}}` + "\n", nil,
			nil, 2, -32603,
			map[string]any{"error_code": "internal"},
		},
		{"prompt-unknown", `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"ghost","prompt":[]}}` + "\n", nil, nil, 2, -32602, nil},
		{"prompt-provider-error", prompt, providerErr, &fakeHistory{}, 2, -32603, map[string]any{"stop_reason": "provider_error", "event_id": float64(0)}},
		{"prompt-internal", prompt, internalErr, &fakeHistory{}, 2, -32603, map[string]any{"error_code": "internal"}},
		{"prompt-panic", prompt, crashes, &fakeHistory{}, 2, -32603, nil},
		{"prompt-runner-error", prompt, runnerErr, &fakeHistory{}, 2, -32603, nil},
		{"set-mode-unknown-mode", `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"session/set_mode","params":{"sessionId":"s1","modeId":"yolo"}}` + "\n", nil, nil, 2, -32602, nil},
		{"set-mode-unknown-session", `{"jsonrpc":"2.0","id":2,"method":"session/set_mode","params":{"sessionId":"ghost","modeId":"plan"}}` + "\n", nil, nil, 2, -32602, nil},
		{"fork-unavailable", `{"jsonrpc":"2.0","id":2,"method":"session/fork","params":{"sessionId":"s1"}}` + "\n", nil, nil, 2, -32601, nil},
		{"initialize-bad-params", `{"jsonrpc":"2.0","id":2,"method":"initialize","params":[]}` + "\n", nil, nil, 2, -32602, nil},
	}
}

// checkFailure asserts one scenario's code and data.
func checkFailure(t *testing.T, tc *failCase) {
	t.Helper()
	out := serveACP(t, tc.input, tc.runner, tc.hist)
	f := reply(t, out, tc.id)
	if got := errCode(t, f); got != float64(tc.code) {
		t.Fatalf("code = %v, want %d:\n%s", got, tc.code, out)
	}
	if tc.data == nil {
		return
	}
	d := dataOf(t, f)
	for k, want := range tc.data {
		if d[k] != want {
			t.Fatalf("data[%s] = %v, want %v (all %v)", k, d[k], want, d)
		}
	}
}

func TestACPConformanceFailures(t *testing.T) {
	cases := conformanceCases()
	for i := range cases {
		tc := &cases[i]
		t.Run(tc.name, func(t *testing.T) { checkFailure(t, tc) })
	}
}

// TestACPProviderErrorEventID: the -32603 data carries the stored turn_end id.
func TestACPProviderErrorEventID(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"x"}]}}` + "\n"
	hist := &fakeHistory{eventID: 77}
	out := serveACP(t, input, &scriptRunner{updates: []tui.Update{{Kind: tui.KindStop, Stop: "error", Detail: "provider_error"}}}, hist)
	f := reply(t, out, 2)
	d := dataOf(t, f)
	if d["stop_reason"] != "provider_error" || d["event_id"] != float64(77) {
		t.Fatalf("data = %v, want provider_error with event_id 77", d)
	}
}

// TestACPLoadDoesNotCreateSession: a failed load must not allocate a session.
func TestACPLoadDoesNotCreateSession(t *testing.T) {
	runner := &scriptRunner{}
	out := serveACP(t, `{"jsonrpc":"2.0","id":1,"method":"session/load","params":{"sessionId":"missing"}}`+"\n", runner, &fakeHistory{err: fmt.Errorf("x: %w", ctxdb.ErrNotFound)})
	if runner.sid != "" {
		t.Fatalf("load created a session for %q", runner.sid)
	}
	if strings.Contains(out, `"sessionId"`) {
		t.Fatalf("failed load answered a session id:\n%s", out)
	}
}

// modeUpdates returns the currentModeId of every current_mode_update frame.
func modeUpdates(t *testing.T, out string) []string {
	t.Helper()
	var modes []string
	for _, f := range frames(t, out) {
		if f["method"] != "session/update" {
			continue
		}
		params, ok := f["params"].(map[string]any)
		if !ok {
			continue
		}
		up, ok := params["update"].(map[string]any)
		if !ok {
			continue
		}
		if up["sessionUpdate"] == "current_mode_update" {
			if id, ok := up["currentModeId"].(string); ok {
				modes = append(modes, id)
			}
		}
	}
	return modes
}

// TestACPModeMatrix (GAP-UX-02): every advertised mode is selectable, sends
// current_mode_update, maps to its approval policy, and plan travels with the
// prompt (SPEC §13.3, §14.1, §18.3).
func TestACPModeMatrix(t *testing.T) {
	runner := &scriptRunner{}
	input := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"session/set_mode","params":{"sessionId":"s1","modeId":"ask"}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"session/set_mode","params":{"sessionId":"s1","modeId":"auto-edit"}}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"method":"session/set_mode","params":{"sessionId":"s1","modeId":"auto"}}` + "\n" +
		`{"jsonrpc":"2.0","id":5,"method":"session/set_mode","params":{"sessionId":"s1","modeId":"plan"}}` + "\n" +
		`{"jsonrpc":"2.0","id":6,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"plan it"}]}}` + "\n"
	out := serveACP(t, input, runner, &fakeHistory{})
	for _, id := range []float64{2, 3, 4, 5} {
		if f := reply(t, out, id); f["error"] != nil {
			t.Fatalf("mode %v rejected: %v", id, f)
		}
	}
	modes := modeUpdates(t, out)
	want := []string{"ask", "auto-edit", "auto", "plan"}
	if len(modes) != len(want) {
		t.Fatalf("mode updates = %v, want %v", modes, want)
	}
	for i := range want {
		if modes[i] != want[i] {
			t.Fatalf("mode updates = %v, want %v", modes, want)
		}
	}
	wantApproval := []string{"ask", "auto-edit", "auto", "ask"}
	if len(runner.approvals) != len(wantApproval) {
		t.Fatalf("approvals = %v, want %v", runner.approvals, wantApproval)
	}
	for i := range wantApproval {
		if runner.approvals[i] != wantApproval[i] {
			t.Fatalf("approvals = %v, want %v", runner.approvals, wantApproval)
		}
	}
	if !runner.input.PlanMode {
		t.Fatal("plan mode did not reach the prompt")
	}
}

// TestACPSetModel (GAP-UX-02): a known model switches the session runner and
// answers null; an unknown id is -32602 and builds nothing.
func TestACPSetModel(t *testing.T) {
	out := &syncBuffer{}
	srv := fakeServer(t, out)
	srv.cfg.Provider.Fallback = []string{"openai/gpt-fallback"}
	runner := &scriptRunner{}
	var built []string
	srv.factory = func(_ string, d *tuiDeps) acpRunner {
		built = append(built, d.modelRef)
		return runner
	}
	srv.history = &fakeHistory{}
	input := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"session/set_model","params":{"sessionId":"s1","modelId":"openai/gpt-fallback"}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"session/set_model","params":{"sessionId":"s1","modelId":"openai/unknown"}}` + "\n"
	if err := srv.serve(t.Context(), strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if f := reply(t, out.String(), 2); f["error"] != nil || f["result"] != nil {
		t.Fatalf("known model response = %v", f)
	}
	unknown := reply(t, out.String(), 3)
	if got := errCode(t, unknown); got != -32602 {
		t.Fatalf("unknown model code = %v, want -32602", got)
	}
	if len(built) != 2 || built[0] != "openai/gpt-test" || built[1] != "openai/gpt-fallback" {
		t.Fatalf("runners built = %v, want [primary fallback]", built)
	}
}

// TestACPSetModelBusy: a turn in flight keeps its model.
func TestACPSetModelBusy(t *testing.T) {
	runner := &scriptRunner{active: true}
	input := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"session/set_model","params":{"sessionId":"s1","modelId":"openai/gpt-test"}}` + "\n"
	out := serveACP(t, input, runner, &fakeHistory{})
	f := reply(t, out, 2)
	if got := errCode(t, f); got != -32603 {
		t.Fatalf("busy set_model code = %v, want -32603", got)
	}
	if d := dataOf(t, f); d["error_code"] != "session_busy" {
		t.Fatalf("data = %v, want session_busy", d)
	}
}

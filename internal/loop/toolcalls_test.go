package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestCallBuilder mirrors the provider assembler's routing rules
// (EC-PROV-001): repeated index, a repeated id on a new index, and an
// id-less continuation all form one call.
func TestCallBuilder(t *testing.T) {
	t.Parallel()
	var b callBuilder
	var got []event.Update
	b.add([]ToolCallDelta{
		{Index: 0, HasIndex: true, ID: "call_9", Name: "edit_", Arguments: `{"pa`},
		{Index: 0, HasIndex: true, Name: "file", Arguments: `th":`},
		{Index: 1, HasIndex: true, ID: "call_9", Arguments: `"a.go"`},
		{Index: 2, HasIndex: true, Arguments: `}`},
	}, func(u *event.Update) { got = append(got, *u) })
	calls := b.calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].ID != "call_9" || calls[0].Name != "edit_file" || calls[0].Arguments != `{"path":"a.go"}` {
		t.Errorf("call = %+v", calls[0])
	}
	var starts, args int
	for _, u := range got {
		switch u.Kind {
		case event.UpdateToolCallStart:
			starts++
		case event.UpdateToolCallArgs:
			args++
		case event.UpdateTextDelta, event.UpdateToolCallEnd, event.UpdateToolResult,
			event.UpdateUsage, event.UpdateNotice, event.UpdateStop:
		}
	}
	if starts != 1 || args != 4 {
		t.Errorf("start updates = %d, args updates = %d", starts, args)
	}
	// Two parallel calls stay separate.
	var p callBuilder
	p.add([]ToolCallDelta{
		{Index: 0, HasIndex: true, ID: "a", Name: "x", Arguments: `{}`},
		{Index: 1, HasIndex: true, ID: "b", Name: "y", Arguments: `{}`},
	}, func(*event.Update) {})
	if calls := p.calls(); len(calls) != 2 || calls[0].ID != "a" || calls[1].ID != "b" {
		t.Errorf("parallel calls = %+v", calls)
	}
}

// TestNormalizeArgs rejects invalid or duplicate-key arguments without
// interpreting them; null and empty stay as sent (EC-TCALL-008).
func TestNormalizeArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    string
		want  string
		code  string
		valid bool
	}{
		{"", "{}", "", true},
		{"  ", "{}", "", true},
		{"null", "null", "", true},
		{`{"a":1}`, `{"a":1}`, "", true},
		{`{"a":1,"a":2}`, "", CodeInvalidArgs, false},
		{`{"a":{"b":1,"b":2}}`, "", CodeInvalidArgs, false},
		{`{"a":[{"b":1},{"b":1}]}`, `{"a":[{"b":1},{"b":1}]}`, "", true},
		{`{"a":}`, "", CodeInvalidArgs, false},
		{"not json", "", CodeInvalidArgs, false},
	}
	for _, c := range cases {
		got, err := normalizeArgs(c.in)
		switch {
		case c.valid && err != nil:
			t.Errorf("%q: unexpected %v", c.in, err)
		case c.valid && got != c.want:
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		case !c.valid && (err == nil || err.Code != c.code):
			t.Errorf("%q: got %v, want %s", c.in, err, c.code)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"read_file":                        "read_file",
		"session_search\" parameter=\"x\"": "session_search",
		"a<b":                              "a",
		"":                                 "",
		"x.y":                              "x",
		"tool-1_2":                         "tool-1_2",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestToolCallAssemblyTurnsEndToEnd: fragments from the provider stream are
// dispatched as one call with the joined arguments (EC-PROV-001).
func TestToolCallAssemblyTurnsEndToEnd(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		emit(Delta{ToolCalls: []ToolCallDelta{
			{Index: 0, HasIndex: true, ID: "c", Name: "read_", Arguments: `{"pa`},
			{Index: 0, HasIndex: true, Name: "file", Arguments: `th":"a.go"}`},
		}})
		return Reply{FinishReason: FinishToolCalls}, nil
	}}
	f.tools.add("read_file", &toolStub{dispatch: func(_ context.Context, p *Prepared) (ToolResult, error) {
		if p.Call.Arguments != `{"path":"a.go"}` {
			t.Errorf("arguments = %q", p.Call.Arguments)
		}
		return ToolResult{Content: "ok"}, nil
	}})
	// The second script is the repeated last one; the turn would loop until
	// the iteration budget. Stop after one dispatch via no_progress.
	f.cfg.MaxIdentical = 1
	got := terminal(t, f.run(t))
	if got.Stop != event.StopEndTurn || got.Detail != event.DetailNoProgress {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
	if f.tools.ranCount() != 1 {
		t.Errorf("dispatched %d calls, want 1", f.tools.ranCount())
	}
}

// TestDuplicateJSONKeysRejected: the duplicate-key check is applied before
// Prepare; the model gets invalid_args and nothing runs.
func TestDuplicateJSONKeysRejected(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
		ToolCall{ID: "a", Name: "read", Arguments: `{"p":1,"p":2}`})}
	f.tools.add("read", &toolStub{})
	f.cfg.MaxIdentical = 1
	ups := f.run(t)
	ev, ok := f.store.find(event.ToolRejected)
	if !ok {
		t.Fatalf("no tool_rejected event: %+v", ups)
	}
	var p auditPayload
	payloadOf(t, &ev, &p)
	if p.Code != CodeInvalidArgs || !strings.Contains(p.Message, "duplicate") {
		t.Errorf("payload = %+v", p)
	}
	if f.tools.ranCount() != 0 {
		t.Errorf("invalid args were dispatched")
	}
}

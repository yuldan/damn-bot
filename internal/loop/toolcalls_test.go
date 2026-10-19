package loop

import (
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

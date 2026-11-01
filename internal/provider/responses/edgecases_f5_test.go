package responses

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestToolCallIDAliases (EC-TCALL-004): every id alias of one function_call
// (call_id, id, item_id) resolves to the same ToolCall, with call_id first
// when several are present.
func TestToolCallIDAliases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-TCALL-004", "F5", "covered") {
		return
	}
	cases := []struct {
		name string
		item string
		want string
	}{
		{"call_id wins over id", `{"type":"function_call","call_id":"call_1","id":"fc_1","name":"f","arguments":"{}"}`, "call_1"},
		{"id wins when call_id absent", `{"type":"function_call","id":"fc_2","item_id":"item_2","name":"f","arguments":"{}"}`, "fc_2"},
		{"item_id is the last alias", `{"type":"function_call","item_id":"item_3","name":"f","arguments":"{}"}`, "item_3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &assembler{}
			if err := a.outputItem([]byte(tc.item)); err != nil {
				t.Fatalf("outputItem: %v", err)
			}
			if len(a.tools) != 1 || a.tools[0].ID != tc.want {
				t.Fatalf("tools = %+v, want id %q", a.tools, tc.want)
			}
		})
	}
}

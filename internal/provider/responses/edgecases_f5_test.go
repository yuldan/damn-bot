//go:build !minimal

package responses

import (
	"encoding/json"
	"testing"
	"time"

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

// TestRotatingItemIDKeepsCallID (EC-PROV-003): a Responses/Codex stream that
// rotates the item id between events still correlates the function call by
// call_id, so one call survives with the id the caller must answer. The
// negative control shows two different call ids stay two calls, i.e. item_id
// never becomes the join key.
func TestRotatingItemIDKeepsCallID(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-003", "F5", "covered") {
		return
	}
	item := func(id, itemID, callID, args string) json.RawMessage {
		return json.RawMessage(`{"type":"function_call","id":"` + id + `","item_id":"` + itemID +
			`","call_id":"` + callID + `","name":"f","arguments":"` + args + `"}`)
	}
	t.Run("EC-PROV-003", func(t *testing.T) {
		t.Parallel()
		a := &assembler{}
		// The intermediate events carry the pre-rotation ids; the adapter
		// ignores them and reads the terminal output list for correlation.
		for _, ev := range []*streamEvent{
			{Type: "response.output_item.added", Item: item("fc_1", "item_1", "call_1", "{}")},
			{Type: "response.output_item.done", Item: item("fc_2", "item_2", "call_1", "{}")},
		} {
			if err := a.add(ev, nil, time.Now(), nil); err != nil {
				t.Fatalf("add %s: %v", ev.Type, err)
			}
		}
		done := &streamEvent{Type: "response.completed", Response: &wireResponse{
			Status: "completed",
			Output: []json.RawMessage{item("fc_3", "item_3", "call_1", `{\"x\":1}`)},
		}}
		if err := a.add(done, nil, time.Now(), nil); err != nil {
			t.Fatalf("add completed: %v", err)
		}
		resp, err := a.result()
		if err != nil {
			t.Fatalf("result: %v", err)
		}
		if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].ArgumentsRaw != `{"x":1}` {
			t.Fatalf("tool calls = %+v, want one call_1 with the final arguments", resp.ToolCalls)
		}
	})
	t.Run("different call ids stay distinct", func(t *testing.T) {
		t.Parallel()
		a := &assembler{}
		done := &streamEvent{Type: "response.completed", Response: &wireResponse{
			Status: "completed",
			Output: []json.RawMessage{
				item("fc_1", "item_1", "call_1", "{}"),
				item("fc_2", "item_2", "call_2", "{}"),
			},
		}}
		if err := a.add(done, nil, time.Now(), nil); err != nil {
			t.Fatalf("add completed: %v", err)
		}
		resp, err := a.result()
		if err != nil {
			t.Fatalf("result: %v", err)
		}
		if len(resp.ToolCalls) != 2 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[1].ID != "call_2" {
			t.Fatalf("tool calls = %+v, want call_1 and call_2", resp.ToolCalls)
		}
	})
}

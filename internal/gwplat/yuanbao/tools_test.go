package yuanbao

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestF7TOOL049YuanbaoGroupInfo covers the group-info tool.
func TestF7TOOL049YuanbaoGroupInfo(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t)
	ad, rec := testAdapter(t, f, &Config{})
	f.push(t, &Frame{Type: "message", EventID: "e1", ChatType: "group", ChatID: "g1", ChatName: "Team", SenderID: "u1", Text: "x", Mentioned: true})
	waitInbound(t, rec.in)
	tool, ok := ad.Tool(ToolGroupInfo)
	if !ok {
		t.Fatal("group info missing")
	}
	out, err := tool.Invoke(t.Context(), json.RawMessage(`{"group":"g1"}`))
	if err != nil || !strings.Contains(out, `"Team"`) {
		t.Fatalf("group info = %q, %v", out, err)
	}
	if _, err := tool.Invoke(t.Context(), json.RawMessage(`{"group":"nope"}`)); !errors.Is(err, ErrGroupUnknown) {
		t.Fatalf("unknown group = %v", err)
	}
}

// TestF7TOOL050YuanbaoGroupMembers covers the member-list tool.
func TestF7TOOL050YuanbaoGroupMembers(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t)
	ad, rec := testAdapter(t, f, &Config{})
	f.push(t, &Frame{
		Type: "message", EventID: "e1", ChatType: "group", ChatID: "g1", SenderID: "u1", Text: "x", Mentioned: true,
		Members: []Member{{ID: "u1", Name: "Ana"}, {ID: "u2", Name: "Bo"}},
	})
	waitInbound(t, rec.in)
	tool, _ := ad.Tool(ToolGroupMembers)
	out, err := tool.Invoke(t.Context(), json.RawMessage(`{"group":"g1"}`))
	if err != nil || !strings.Contains(out, `"Ana"`) || !strings.Contains(out, `"Bo"`) {
		t.Fatalf("members = %q, %v", out, err)
	}
}

// TestF7TOOL051YuanbaoSearchSticker covers sticker search and the empty set.
func TestF7TOOL051YuanbaoSearchSticker(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t)
	ad, _ := testAdapter(t, f, &Config{})
	tool, _ := ad.Tool(ToolSearchSticker)
	out, err := tool.Invoke(t.Context(), json.RawMessage(`{"query":"hi"}`))
	if err != nil || !strings.Contains(out, `"s1"`) {
		t.Fatalf("search = %q, %v", out, err)
	}
	out, err = tool.Invoke(t.Context(), json.RawMessage(`{"query":"absent"}`))
	if err != nil || out != "[]" {
		t.Fatalf("empty search = %q, %v", out, err)
	}
}

func invokeYBTool(t *testing.T, ad *Adapter, name, args string) (string, error) {
	t.Helper()
	tool, ok := ad.Tool(name)
	if !ok {
		t.Fatalf("tool %s missing", name)
	}
	return tool.Invoke(t.Context(), json.RawMessage(args))
}

func assertYBToolSend(t *testing.T, f *fakeServer, ad *Adapter, name, okArgs, badArgs string, frameOK func(Frame) bool, wantErr error) {
	t.Helper()
	out, err := invokeYBTool(t, ad, name, okArgs)
	if err != nil || out != "sent" {
		t.Fatalf("%s = %q, %v", name, out, err)
	}
	if frame := waitFrame(t, f.fromClient); !frameOK(frame) {
		t.Fatalf("%s frame = %+v", name, frame)
	}
	if _, err := invokeYBTool(t, ad, name, badArgs); !errors.Is(err, wantErr) {
		t.Fatalf("%s rejection = %v, want %v", name, err, wantErr)
	}
}

// TestF7TOOL052YuanbaoSendDM covers the allowlisted DM send and its denial.
func TestF7TOOL052YuanbaoSendDM(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t)
	ad, _ := testAdapter(t, f, &Config{})
	assertYBToolSend(t, f, ad, ToolSendDM,
		`{"user":"u1","text":"hi"}`, `{"user":"stranger","text":"hi"}`,
		func(frame Frame) bool { return frame.ChatID == "dm:u1" }, ErrDMNotAllowed)
}

// TestF7TOOL053YuanbaoSendSticker covers the catalogued sticker send and the
// rejection of a foreign sticker id.
func TestF7TOOL053YuanbaoSendSticker(t *testing.T) {
	t.Parallel()
	f := newFakeServer(t)
	ad, _ := testAdapter(t, f, &Config{})
	assertYBToolSend(t, f, ad, ToolSendSticker,
		`{"chat":"g1","sticker_id":"s1"}`, `{"chat":"g1","sticker_id":"foreign"}`,
		func(frame Frame) bool { return frame.StickerID == "s1" }, ErrStickerUnknown)
}

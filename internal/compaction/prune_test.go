package compaction

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// transcript builds events in seq order the way the loop records them.
type transcript struct {
	t      *testing.T
	events []Event
	turn   int
	calls  int
}

func (b *transcript) add(e *Event) int64 {
	e.Seq = int64(len(b.events) + 1)
	e.TurnID = fmt.Sprintf("t%d", b.turn)
	b.events = append(b.events, *e)
	return e.Seq
}

func (b *transcript) user(text string, images ...string) {
	b.turn++
	b.add(&Event{Kind: event.TurnStart, Payload: `{}`})
	b.add(&Event{Kind: event.UserMessage, Role: "user", Content: text, Images: images})
}

func (b *transcript) assistant(text string, calls ...ToolCall) int64 {
	b.add(&Event{Kind: event.LLMCall, Payload: `{"model":"m"}`})
	raw, err := json.Marshal(map[string]any{"tool_calls": calls})
	if err != nil {
		b.t.Fatal(err)
	}
	return b.add(&Event{Kind: event.AssistantMessage, Role: "assistant", Content: text, Payload: string(raw)})
}

// call is one round: an assistant message with one call and its result.
func (b *transcript) call(tool, args, content string) (id string, seq int64) {
	b.calls++
	id = fmt.Sprintf("c%d", b.calls)
	b.assistant("", ToolCall{ID: id, Name: tool, Arguments: args})
	return id, b.result(id, tool, content, `{}`)
}

func (b *transcript) result(id, tool, content, payload string) int64 {
	return b.add(&Event{Kind: event.ToolResult, Role: "tool", Content: content, ToolCallID: id, ToolName: tool, Payload: payload})
}

func contentOf(t *testing.T, msgs []Message, callID string) string {
	t.Helper()
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID == callID {
			return m.Content
		}
	}
	t.Fatalf("no result for %s", callID)
	return ""
}

func TestPruneDedupe(t *testing.T) {
	t.Parallel()
	b := transcript{t: t}
	b.user("find it")
	first, _ := b.call("grep", `{"pattern":"x"}`, strings.Repeat("hit\n", 20))
	last, _ := b.call("grep", `{"pattern":"x"}`, strings.Repeat("hit\n", 20))
	other, _ := b.call("grep", `{"pattern":"y"}`, strings.Repeat("hit\n", 20))
	msgs, st := Prune(b.events)
	if got := contentOf(t, msgs, first); got != "[dup of call "+last+"]" {
		t.Errorf("older duplicate = %q", got)
	}
	if contentOf(t, msgs, last) != strings.Repeat("hit\n", 20) || contentOf(t, msgs, other) != strings.Repeat("hit\n", 20) {
		t.Error("newest duplicate or a different call was pruned")
	}
	if st.Dups != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestPruneStaleRead(t *testing.T) {
	t.Parallel()
	b := transcript{t: t}
	b.user("edit")
	read, _ := b.call("read_file", `{"path":"a.go"}`, strings.Repeat("package a\n", 10))
	keep, _ := b.call("read_file", `{"path":"b.go"}`, strings.Repeat("package b\n", 10))
	b.assistant("", ToolCall{ID: "bad", Name: "edit_file", Arguments: `{"path":"b.go"}`})
	b.result("bad", "edit_file", "no match", `{"error":{"code":"not_found","message":"x"}}`)
	_, mut := b.call("edit_file", `{"path":"a.go"}`, "ok")
	b.call("write_file", `{"path":"a.go"}`, "ok")
	msgs, st := Prune(b.events)
	if got := contentOf(t, msgs, read); got != fmt.Sprintf("[stale: file modified at seq %d]", mut) {
		t.Errorf("stale read = %q", got)
	}
	if contentOf(t, msgs, keep) != strings.Repeat("package b\n", 10) {
		t.Error("a failed mutation made a read stale")
	}
	if st.Stale != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestPruneOldResults(t *testing.T) {
	t.Parallel()
	b := transcript{t: t}
	b.user("survey")
	big := strings.Repeat("x", 400)
	read, _ := b.call("read_file", `{"path":"docs/a.md"}`, big)
	search, _ := b.call("tool_search", `{"query":"pdf"}`, big+"schema")
	b.assistant("", ToolCall{ID: "sh", Name: "shell", Arguments: `{"command":"ls"}`})
	b.add(&Event{
		Kind: event.ToolResult, Role: "tool", Content: big + "ls", ToolCallID: "sh", ToolName: "shell",
		BlobSHA: strings.Repeat("ab", 32), Payload: `{}`,
	})
	small, _ := b.call("glob", `{"pattern":"*"}`, "a")
	for i := range keepResults {
		b.call("grep", fmt.Sprintf(`{"pattern":"%d"}`, i), big+fmt.Sprint(i))
	}
	msgs, st := Prune(b.events)
	if got := contentOf(t, msgs, read); got != "[pruned: 400 chars; read_file docs/a.md or recall]" {
		t.Errorf("old read = %q", got)
	}
	if got := contentOf(t, msgs, "sh"); got != "[pruned: 402 chars, blob abababababab; recall]" {
		t.Errorf("old spilled shell = %q", got)
	}
	if contentOf(t, msgs, search) != big+"schema" || contentOf(t, msgs, small) != "a" {
		t.Error("tool_search or a result shorter than its stub was pruned")
	}
	if st.Old != 2 {
		t.Errorf("stats = %+v", st)
	}
}

func TestPruneTrimAndImages(t *testing.T) {
	t.Parallel()
	b := transcript{t: t}
	long := strings.Repeat("é", 300)
	b.user("one", "1111111111111111aaaa", "2222222222222222bbbb")
	oldSeq := b.assistant(long, ToolCall{ID: "k0", Name: "glob", Arguments: `{}`})
	b.result("k0", "glob", "a", `{}`)
	answerSeq := b.assistant(long)
	b.user("two", "3333333333333333cccc")
	for i := 1; i <= keepIterations; i++ {
		id := fmt.Sprintf("k%d", i)
		b.assistant(long, ToolCall{ID: id, Name: "glob", Arguments: `{}`})
		b.result(id, "glob", "a", `{}`)
	}
	b.user("three", "4444444444444444dddd")
	msgs, st := Prune(b.events)
	items, _ := history(b.events)
	for i, it := range items {
		switch it.seq {
		case oldSeq:
			if got := []rune(msgs[i].Content); len(got) != trimRunes || got[trimRunes-1] != '…' {
				t.Errorf("old assistant text = %d runes", len(got))
			}
		case answerSeq:
			if msgs[i].Content != long {
				t.Error("a final answer was trimmed")
			}
		}
	}
	if msgs[0].Content != "one\n[image 111111111111 omitted]\n[image 222222222222 omitted]" || msgs[0].Images != nil {
		t.Errorf("old images = %q %v", msgs[0].Content, msgs[0].Images)
	}
	last := msgs[len(msgs)-1]
	if len(last.Images) != 1 || msgs[4].Role != "user" || len(msgs[4].Images) != 1 {
		t.Errorf("images of the newest %d turns were dropped", keepImageTurns)
	}
	if st.Trimmed != 1 || st.Images != 2 {
		t.Errorf("stats = %+v", st)
	}
}

// TestPrunePairing: every tool call keeps exactly one result right after
// its assistant message, roles keep their order, untrusted wrappers stay
// balanced, the pending round is untouched, and Prune is deterministic.
func TestPrunePairing(t *testing.T) {
	t.Parallel()
	b := sampleTranscript(t, 40)
	want, _ := history(b.events)
	msgs, _ := Prune(b.events)
	again, _ := Prune(b.events)
	if !reflect.DeepEqual(msgs, again) {
		t.Fatal("Prune is not deterministic")
	}
	if len(msgs) != len(want) {
		t.Fatalf("messages = %d, want %d", len(msgs), len(want))
	}
	open := map[string]bool{}
	for i, m := range msgs {
		if m.Role != want[i].Role || m.ToolCallID != want[i].ToolCallID {
			t.Fatalf("message %d changed shape: %+v", i, m)
		}
		if strings.Count(m.Content, "<untrusted") != strings.Count(m.Content, "</untrusted>") {
			t.Errorf("message %d has an unbalanced untrusted wrapper: %q", i, m.Content)
		}
		for _, c := range m.ToolCalls {
			open[c.ID] = true
		}
		if m.Role == "tool" {
			if !open[m.ToolCallID] {
				t.Errorf("result %s without an open call", m.ToolCallID)
			}
			delete(open, m.ToolCallID)
		}
	}
	if len(open) != 0 {
		t.Errorf("calls without result: %v", open)
	}
}

// sampleTranscript is n rounds of mixed tools, untrusted output and repeats,
// ending in a parallel round the model has not read yet.
func sampleTranscript(t *testing.T, n int) *transcript {
	t.Helper()
	b := transcript{t: t}
	b.user("start")
	for i := range n {
		switch i % 4 {
		case 0:
			b.call("read_file", `{"path":"f.go"}`, strings.Repeat("line\n", 30))
		case 1:
			b.call("shell", `{"command":"curl"}`, `<untrusted source="shell:network">`+strings.Repeat("net ", 40)+`</untrusted>`)
		case 2:
			b.call("edit_file", `{"path":"f.go"}`, "ok")
		default:
			b.user(fmt.Sprintf("next %d", i))
		}
	}
	b.assistant("", ToolCall{ID: "p1", Name: "read_file", Arguments: `{"path":"f.go"}`},
		ToolCall{ID: "p2", Name: "read_file", Arguments: `{"path":"f.go"}`},
		ToolCall{ID: "p3", Name: "write_file", Arguments: `{"path":"f.go"}`})
	b.result("p1", "read_file", strings.Repeat("line\n", 30), `{}`)
	b.result("p2", "read_file", strings.Repeat("line\n", 30), `{}`)
	b.result("p3", "write_file", "ok", `{}`)
	return &b
}

func TestPrunePendingRoundUntouched(t *testing.T) {
	t.Parallel()
	b := sampleTranscript(t, 12)
	msgs, _ := Prune(b.events)
	for _, id := range []string{"p1", "p2"} {
		if got := contentOf(t, msgs, id); got != strings.Repeat("line\n", 30) {
			t.Errorf("pending result %s = %q", id, got)
		}
	}
}

func TestPruneHistoryNormalisation(t *testing.T) {
	t.Parallel()
	b := transcript{t: t}
	b.user("go")
	b.result("ghost", "shell", "orphan output", `{}`)
	b.assistant("", ToolCall{ID: "c1", Name: "shell", Arguments: `{}`})
	b.result("c1", "shell", "", `{"ok":true}`) // dispatcher journal row
	b.result("c1", "shell", "real output", `{"blob_sha":""}`)
	b.assistant("", ToolCall{ID: "c1", Name: "shell", Arguments: `{}`}) // reused id
	b.result("c1", "shell", "second", `{}`)
	b.add(&Event{Kind: event.AssistantMessage, Role: "assistant", Content: "bad payload", Payload: `{`})
	msgs, st := Prune(b.events)
	got := make([]string, 0, len(msgs))
	for _, m := range msgs {
		got = append(got, m.Role+":"+m.Content)
	}
	want := []string{"user:go", "assistant:", "tool:real output", "assistant:", "tool:second", "assistant:bad payload"}
	if !reflect.DeepEqual(got, want) || st.Orphans != 1 {
		t.Errorf("history = %q, orphans %d", got, st.Orphans)
	}
}

func TestResultFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		payload, content string
		failed, tainted  bool
	}{
		{`{}`, "x", false, false},
		{`{"ok":false}`, "x", true, false},
		{`{"ok":true,"error":null}`, "x", false, false},
		{`{"error":{"code":"denied"}}`, "x", true, false},
		{`{"tainted":true}`, "x", false, true},
		{`not json`, `<untrusted source="web">x</untrusted>`, false, true},
	}
	for _, c := range cases {
		f, tn := resultFlags(&Event{Payload: c.payload, Content: c.content})
		if f != c.failed || tn != c.tainted {
			t.Errorf("resultFlags(%s, %q) = %v %v", c.payload, c.content, f, tn)
		}
	}
}

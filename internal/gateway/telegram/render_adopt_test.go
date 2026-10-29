package telegram

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/gateway/render"
)

// sentMessages returns the text/entities form of every fake send.
func sentMessages(t *testing.T, f *fakeTG) ([]string, [][]render.Entity) {
	t.Helper()
	var texts []string
	var entities [][]render.Entity
	for _, call := range f.callsFor("sendMessage") {
		texts = append(texts, call.Form.Get("text"))
		var ents []render.Entity
		if raw := call.Form.Get("entities"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &ents); err != nil {
				t.Fatalf("entities JSON: %v", err)
			}
		}
		entities = append(entities, ents)
	}
	return texts, entities
}

// TestSendChunksLongReply: a 10.000-character answer becomes ordered sends of
// at most 4096 UTF-16 units each, with no split surrogate and no loss.
func TestSendChunksLongReply(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	src := strings.Repeat("😀kata ", 1500) // 15.000 UTF-16 units, astral runes
	want := render.RenderTelegram(src, render.Limits{})
	if len(want) < 2 {
		t.Fatalf("renderer produced %d chunks, want several", len(want))
	}
	if _, err := a.Send(t.Context(), &Outbound{ChatID: "7", Text: src}); err != nil {
		t.Fatalf("send: %v", err)
	}
	texts, _ := sentMessages(t, f)
	if len(texts) != len(want) {
		t.Fatalf("sendMessage calls = %d, want %d", len(texts), len(want))
	}
	var joined strings.Builder
	for i, text := range texts {
		if !utf8.ValidString(text) {
			t.Errorf("chunk %d is not valid UTF-8", i)
		}
		if n := render.Units(text); n > render.MaxLen {
			t.Errorf("chunk %d = %d UTF-16 units, want ≤ %d", i, n, render.MaxLen)
		}
		if text != want[i].Text {
			t.Errorf("chunk %d differs from the renderer's chunk", i)
		}
		joined.WriteString(text)
	}
	full := ""
	for _, m := range want {
		full += m.Text
	}
	if joined.String() != full {
		t.Errorf("chunks do not reconstruct the rendered text (%d vs %d bytes)", joined.Len(), len(full))
	}
	if got := f.requests(); got != len(want) {
		t.Errorf("HTTP requests = %d, want %d sends", got, len(want))
	}
}

// TestSendEntitiesUTF16: entities are sent as form JSON with UTF-16 offsets
// matching the text that actually crosses the wire; fenced code keeps its
// language and inline backticks stay `code`.
func TestSendEntitiesUTF16(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	src := "**tebal** dan `kode`\r\n\r\n```go\r\nfmt.Println(\"hi\")\r\n```\r\n"
	if _, err := a.Send(t.Context(), &Outbound{ChatID: "7", Text: src}); err != nil {
		t.Fatalf("send: %v", err)
	}
	texts, ents := sentMessages(t, f)
	if len(texts) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(texts))
	}
	if got := f.callsFor("sendMessage")[0].Form.Get("parse_mode"); got != "" {
		t.Errorf("parse_mode = %q, want unset", got)
	}
	counts := entityCoverage(t, texts[0], ents[0])
	if counts[render.EntityBold] != 1 || counts[render.EntityCode] != 1 || counts[render.EntityPre] != 1 {
		t.Errorf("entities = %v, want one bold, code and pre", counts)
	}
}

// entityCoverage verifies every entity's UTF-16 span against the sent text and
// returns how many of each type were seen. Fenced code must keep its language
// while inline backticks must stay code, not pre.
func entityCoverage(t *testing.T, text string, ents []render.Entity) map[string]int {
	t.Helper()
	units := utf16.Encode([]rune(text))
	counts := map[string]int{}
	for _, e := range ents {
		if e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > len(units) {
			t.Fatalf("entity %+v outside %d UTF-16 units", e, len(units))
		}
		visible := string(utf16.Decode(units[e.Offset : e.Offset+e.Length]))
		counts[e.Type]++
		switch e.Type {
		case render.EntityBold:
			if visible != "tebal" {
				t.Errorf("bold covers %q, want tebal", visible)
			}
		case render.EntityCode:
			if visible != "kode" {
				t.Errorf("code covers %q, want kode (inline backtick)", visible)
			}
		case render.EntityPre:
			if e.Language != "go" {
				t.Errorf("pre language = %q, want go", e.Language)
			}
			if !strings.Contains(visible, "fmt.Println") {
				t.Errorf("pre covers %q, want the fenced body", visible)
			}
		}
	}
	return counts
}

// TestApprovalCardTruncatesAfterEscape: MeasureCard counts the escaped text,
// so a card that fits before escaping but not after is cut to one message and
// keeps its inline keyboard (EC-PLAT-012).
func TestApprovalCardTruncatesAfterEscape(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	a.after = 20 * time.Millisecond
	src := strings.Repeat("\x01", 3000) + strings.Repeat("a", 2000)
	if n := render.MeasureCard(src); n <= render.MaxLen {
		t.Fatalf("MeasureCard = %d, want > %d after escaping", n, render.MaxLen)
	}
	decision, err := a.Approve(t.Context(), "7", "42", src)
	if err != nil || decision != DecisionDeny {
		t.Fatalf("approve = %v/%v, want timeout deny", decision, err)
	}
	calls := f.callsFor("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("sendMessage calls = %d, want one card", len(calls))
	}
	card := calls[0].Form.Get("text")
	if n := render.Units(card); n > render.MaxLen {
		t.Errorf("card = %d UTF-16 units, want ≤ %d", n, render.MaxLen)
	}
	if !strings.HasSuffix(card, render.Truncated) {
		t.Errorf("card does not end with %q", render.Truncated)
	}
	if !strings.Contains(calls[0].Form.Get("reply_markup"), "Allow once") {
		t.Error("truncated card lost its inline keyboard")
	}
	if strings.ContainsRune(card, 0x01) {
		t.Error("raw control character leaked into the card")
	}
	if got := calls[0].Form.Get("reply_to_message_id"); got != "" {
		t.Errorf("approval card reply_to_message_id = %q, want unset", got)
	}
}

// TestAnchorValidation: only a positive numeric message id produces
// reply_to_message_id; a bogus anchor never fails the send (EC-PLAT-018).
func TestAnchorValidation(t *testing.T) {
	for _, tc := range []struct {
		name, anchor, want string
	}{
		{"invalid", "not-a-number", ""},
		{"zero", "0", ""},
		{"spaces", " 42", ""},
		{"valid", "42", "42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeTG(t)
			a, _ := newTestAdapter(t, f, newMemStore())
			if _, err := a.Send(t.Context(), &Outbound{ChatID: "7", Text: "hai", Anchor: tc.anchor}); err != nil {
				t.Fatalf("send with anchor %q: %v", tc.anchor, err)
			}
			calls := f.callsFor("sendMessage")
			if len(calls) != 1 {
				t.Fatalf("sendMessage calls = %d, want 1", len(calls))
			}
			if got := calls[0].Form.Get("reply_to_message_id"); got != tc.want {
				t.Errorf("reply_to_message_id = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStreamEditsRenderedMessage: the stream edit path renders too, and still
// edits the placeholder instead of sending a fresh message.
func TestStreamEditsRenderedMessage(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	stream := a.StreamFrom(&Outbound{ChatID: "7", ThreadID: "42", Key: "reply-1"}, 11, "…")
	if err := stream.Update(t.Context(), "**halo** `dunia`"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if n := len(f.callsFor("sendMessage")); n != 0 {
		t.Fatalf("sendMessage calls = %d, want 0 (an edit only)", n)
	}
	edits := f.callsFor("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("editMessageText calls = %d, want 1", len(edits))
	}
	if got := formInt(edits[0].Form, "message_id"); got != 11 {
		t.Errorf("edit message_id = %d, want 11", got)
	}
	if got := edits[0].Form.Get("text"); got != "halo dunia" {
		t.Errorf("edit text = %q, want rendered plain text", got)
	}
	if got := edits[0].Form.Get("parse_mode"); got != "" {
		t.Errorf("edit parse_mode = %q, want unset", got)
	}
	if edits[0].Form.Get("entities") == "" {
		t.Error("edit lost its entities")
	}
}

// TestStreamAppendsChunksOnce: overflow chunks are sent once and then edited
// in place, never duplicated on later updates.
func TestStreamAppendsChunksOnce(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	stream := a.Stream(&Outbound{ChatID: "7", Key: "stream-long"})
	src := strings.Repeat("kata ", 2000)
	if err := stream.Update(t.Context(), src); err != nil {
		t.Fatalf("first update: %v", err)
	}
	sends := len(f.callsFor("sendMessage"))
	if sends < 2 {
		t.Fatalf("sendMessage calls = %d, want the reply split", sends)
	}
	if err := stream.Update(t.Context(), src+" akhir"); err != nil {
		t.Fatalf("second update: %v", err)
	}
	if got := len(f.callsFor("sendMessage")); got != sends {
		t.Errorf("second update sent %d new messages, want 0", got-sends)
	}
	edits := f.callsFor("editMessageText")
	if len(edits) != sends {
		t.Errorf("editMessageText calls = %d, want %d (one per message)", len(edits), sends)
	}
	seen := map[int64]bool{}
	for _, e := range edits {
		seen[formInt(e.Form, "message_id")] = true
	}
	if len(seen) != sends {
		t.Errorf("edited %d distinct messages, want %d", len(seen), sends)
	}
}

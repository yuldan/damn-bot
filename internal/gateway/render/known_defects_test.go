package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// REL-14 known-defect conformance: each scenario pins one historical delivery
// defect to its current correct behaviour.

func TestKnownDefectLongTextChunked(t *testing.T) {
	msgs := RenderTelegram(strings.Repeat("abcdefghij", 1000), Limits{})
	if len(msgs) < 2 {
		t.Fatalf("chunks = %d, want >1", len(msgs))
	}
	for _, m := range msgs {
		if n := Units(m.Text); n > MaxLen {
			t.Fatalf("chunk units = %d", n)
		}
	}
}

func TestKnownDefectChunkCountBounded(t *testing.T) {
	msgs := RenderTelegram(strings.Repeat("x ", 20000), Limits{MaxChunks: 3})
	if len(msgs) > 3 || !strings.Contains(msgs[len(msgs)-1].Text, Truncated) {
		t.Fatalf("chunks = %d, last = %q", len(msgs), msgs[len(msgs)-1].Text)
	}
}

func TestKnownDefectEmojiNotSplit(t *testing.T) {
	for _, m := range RenderTelegram(strings.Repeat("😀", 3000), Limits{}) {
		if !utf8.ValidString(m.Text) {
			t.Fatal("invalid UTF-8 chunk")
		}
	}
}

func TestKnownDefectFenceBalanced(t *testing.T) {
	body := "```go\n" + strings.Repeat("fmt.Println(1)\n", 400) + "```"
	for _, m := range RenderTelegram(body, Limits{}) {
		if strings.Count(m.Text, "```")%2 != 0 {
			t.Fatalf("unbalanced fence in %q", m.Text[:min(20, len(m.Text))])
		}
	}
}

func TestKnownDefectExactLimitOneMessage(t *testing.T) {
	if got := len(RenderTelegram(strings.Repeat("a", MaxLen), Limits{})); got != 1 {
		t.Fatalf("chunks = %d", got)
	}
}

func TestKnownDefectEmptyRendersNothing(t *testing.T) {
	if got := RenderTelegram("", Limits{}); len(got) != 0 {
		t.Fatalf("empty = %v", got)
	}
}

func TestKnownDefectEntitiesInsideText(t *testing.T) {
	msgs := RenderTelegram("**bold** and `code`", Limits{})
	units := Units(msgs[0].Text)
	for _, e := range msgs[0].Entities {
		if e.Offset < 0 || e.Offset+e.Length > units {
			t.Fatalf("entity %+v outside %d units", e, units)
		}
	}
}

func TestKnownDefectSmallLimitFitsMarker(t *testing.T) {
	msgs := RenderTelegram(strings.Repeat("y", 200), Limits{MaxLen: 30, MaxChunks: 1})
	if len(msgs) != 1 || Units(msgs[0].Text) > 30 {
		t.Fatalf("msgs = %+v", msgs)
	}
}

func TestKnownDefectCardMeasureEscapes(t *testing.T) {
	if MeasureCard("<b>x</b>") == 0 {
		t.Fatal("empty card measure")
	}
}

func TestKnownDefectPlainFallback(t *testing.T) {
	msgs := RenderTelegram("plain", Limits{})
	if len(msgs) != 1 || msgs[0].Text != "plain" {
		t.Fatalf("plain = %+v", msgs)
	}
}

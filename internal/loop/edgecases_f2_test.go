package loop

import (
	"context"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCasesF2 (EDGE-CASES-hermes.md, ADR-077/080): the F2 entries owned
// by the loop. EC-CTX-017 lives here because the window guard is loop code;
// the compaction catalog keeps only the summarizer entries.
func TestEdgeCasesF2(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(t *testing.T)
	}{
		{"EC-PROV-008", ecProv008},
		{"EC-CTX-017", ecCtx017},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F2", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// EC-PROV-008: finish_reason=length marks the answer partial and it never
// replays as a final assistant message (the transcript keeps it for the user;
// cmd/damnbot's History drops it).
func ecProv008(t *testing.T) {
	f := newFixture()
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		emit(Delta{Text: "half an ans"})
		return Reply{Content: "half an ans", FinishReason: FinishLength, Model: "fake/m"}, nil
	}}
	stop := terminal(t, f.run(t))
	if stop.Stop != event.StopMaxTokens || stop.Detail != event.DetailOutputTruncated {
		t.Fatalf("stop = %s/%s, want max_tokens/output_truncated", stop.Stop, stop.Detail)
	}
	ev, ok := f.store.find(event.AssistantMessage)
	if !ok || ev.Content != "half an ans" {
		t.Fatalf("assistant event = %+v, ok %v", ev, ok)
	}
	var p assistantPayload
	payloadOf(t, &ev, &p)
	if !p.Partial || p.FinishReason != FinishLength {
		t.Fatalf("payload = %+v, want partial with finish_reason=length", p)
	}
}

// EC-CTX-017: the guard budgets input as window - max_tokens, so the output
// reservation is never spent on input (a request that fits the window but not
// the window minus max_tokens is the 400 this prevents).
func ecCtx017(t *testing.T) {
	t.Run("overflow", func(t *testing.T) {
		f := newFixture()
		f.asm.prompt = Prompt{Request: Request{MaxTokens: 200}, InputTokens: 760, WindowTokens: 1000}
		f.cfg.CompactAt, f.cfg.OverflowAt = 75, 90
		stop := terminal(t, f.run(t))
		if stop.Stop != event.StopMaxTokens || stop.Detail != event.DetailContextOverflow {
			t.Fatalf("stop = %s/%s, want max_tokens/context_overflow", stop.Stop, stop.Detail)
		}
		if f.provider.calls != 0 {
			t.Fatalf("provider calls = %d, want 0", f.provider.calls)
		}
	})
	t.Run("compaction", func(t *testing.T) {
		f := newFixture()
		f.asm.prompt = Prompt{Request: Request{MaxTokens: 200}, InputTokens: 620, WindowTokens: 1000}
		f.cfg.CompactAt, f.cfg.OverflowAt = 75, 90
		c := &fakeCompactor{}
		f.compactor = c
		stop := terminal(t, f.run(t))
		if c.calls != 1 {
			t.Fatalf("compactor calls = %d, want 1 (620/800 = 77%%)", c.calls)
		}
		if stop.Stop != event.StopEndTurn {
			t.Fatalf("stop = %s/%s, want end_turn", stop.Stop, stop.Detail)
		}
		if f.provider.calls != 1 {
			t.Fatalf("provider calls = %d, want 1", f.provider.calls)
		}
	})
}

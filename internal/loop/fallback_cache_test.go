package loop

import (
	"context"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestCacheBreakMetric (GAP-COST-04): a fallback marks the llm_call with
// cache_break=model_switch, so a cache miss caused by the switch is visible;
// a normal call carries no cache_break.
func TestCacheBreakMetric(t *testing.T) {
	t.Parallel()
	t.Run("fallback", func(t *testing.T) {
		f := newFixture()
		f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
			emit(Delta{Text: "ok"})
			return Reply{Content: "ok", FinishReason: FinishStop, Model: "b/m", FallbackFrom: "a/m"}, nil
		}}
		f.run(t)
		ev, ok := f.store.find(event.LLMCall)
		if !ok {
			t.Fatal("no llm_call event")
		}
		var p llmPayload
		payloadOf(t, &ev, &p)
		if p.CacheBreak != "model_switch" || p.FallbackFrom != "a/m" || p.Model != "b/m" {
			t.Fatalf("payload = %+v", p)
		}
	})
	t.Run("normal", func(t *testing.T) {
		f := newFixture()
		f.run(t)
		ev, ok := f.store.find(event.LLMCall)
		if !ok {
			t.Fatal("no llm_call event")
		}
		var p llmPayload
		payloadOf(t, &ev, &p)
		if p.CacheBreak != "" {
			t.Fatalf("cache_break = %q on a normal call", p.CacheBreak)
		}
	})
}

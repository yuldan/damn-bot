package loop

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestFailedTurnExcludes: a failed, aborted or panicked turn keeps every row
// it persisted, appends context_exclude{turn_id, reason} before turn_end, and
// records turn_end exactly once (GAP-REL-01).
func TestFailedTurnExcludes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		cancel bool
		script streamFunc
		reason string
	}{
		{"provider_error", false, errorScript(ClassProvider, errors.New("400 invalid_request_error")), "provider_error"},
		{"context_overflow", false, errorScript(ClassContextLength, errors.New("context_length_exceeded")), "context_overflow"},
		{"cancelled", true, nil, "cancelled"},
		{"panic", false, func(context.Context, *Request, func(Delta)) (Reply, error) {
			panic("boom") //nolint:forbidigo // provokes the panic boundary this test asserts.
		}, "panic"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture()
			f.provider.scripts = []streamFunc{c.script}
			if c.cancel {
				f.provider.scripts = []streamFunc{func(ctx context.Context, _ *Request, _ func(Delta)) (Reply, error) {
					f.cancel()
					<-ctx.Done()
					return Reply{}, &StreamError{Class: ClassCancelled, Err: ctx.Err()}
				}}
			}
			f.run(t) // a panic is recovered by internal/boundary
			assertExcluded(t, f, c.reason)
		})
	}
}

// assertExcluded checks the terminal rows of one failed turn.
func assertExcluded(t *testing.T, f *fixture, reason string) {
	t.Helper()
	kinds := f.store.kinds()
	if n := countKind(kinds, event.TurnEnd); n != 1 {
		t.Fatalf("turn_end rows = %d, want exactly 1 (%v)", n, kinds)
	}
	marker, ok := f.store.find(event.ContextExclude)
	if !ok {
		t.Fatalf("no context_exclude marker (%v)", kinds)
	}
	var p excludePayload
	payloadOf(t, &marker, &p)
	if p.TurnID != "T1" || p.Reason != reason {
		t.Fatalf("marker = %+v, want T1/%s", p, reason)
	}
	if !slices.Contains(kinds, event.UserMessage) {
		t.Fatalf("user_message lost (%v)", kinds)
	}
	if i, j := slices.Index(kinds, event.ContextExclude), slices.Index(kinds, event.TurnEnd); i < 0 || j < 0 || i > j {
		t.Fatalf("marker must precede turn_end (%v)", kinds)
	}
}

// TestCompletedTurnNotExcluded: policy stops keep their history; only
// failures and cancellations are hidden from later requests.
func TestCompletedTurnNotExcluded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		script streamFunc
	}{
		{"completed", textScript("done", event.Usage{})},
		{"empty_response", func(context.Context, *Request, func(Delta)) (Reply, error) {
			return Reply{FinishReason: FinishStop, Model: "fake/m"}, nil
		}},
		{"content_filtered", func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
			emit(Delta{Text: "partial"})
			return Reply{Content: "partial", FinishReason: FinishContentFilter}, nil
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture()
			f.provider.scripts = []streamFunc{c.script, c.script}
			f.run(t)
			kinds := f.store.kinds()
			if slices.Contains(kinds, event.ContextExclude) {
				t.Fatalf("policy stop excluded (%v)", kinds)
			}
			if n := countKind(kinds, event.TurnEnd); n != 1 {
				t.Fatalf("turn_end rows = %d (%v)", n, kinds)
			}
		})
	}
}

func countKind(kinds []event.Kind, k event.Kind) int {
	n := 0
	for _, x := range kinds {
		if x == k {
			n++
		}
	}
	return n
}

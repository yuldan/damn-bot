package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases (EDGE-CASES-hermes.md, ADR-077/080/122): the EC-CTX entries
// owned by compaction (F2). Pending entries need a component that is not on
// main yet; ADR-122 names it.
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id, status string
		fn         func(t *testing.T)
	}{
		{"EC-CTX-001", "covered", ecCtx001},
		{"EC-CTX-002", "covered", ecCtx002},
		{"EC-CTX-003", "covered", ecCtx003},
		{"EC-CTX-004", "covered", ecCtx004},
		{"EC-CTX-005", "covered", ecCtx005},
		{"EC-CTX-006", "covered", ecCtx006},
		{"EC-CTX-007", "covered", ecCtx007},
		{"EC-CTX-008", "covered", ecCtx008},
		{"EC-CTX-009", "covered", ecCtx009},
		{"EC-CTX-010", "covered", ecCtx010},
		{"EC-CTX-011", "covered", ecCtx011},
		{"EC-CTX-012", "covered", ecCtx012},
		{"EC-CTX-013", "covered", ecCtx013},
		{"EC-CTX-014", "covered", ecCtx014},
		{"EC-CTX-015", "covered", ecCtx015},
		{"EC-CTX-016", "covered", ecCtx016},
		{"EC-CTX-018", "covered", ecCtx018},
		{"EC-CTX-019", "covered", ecCtx019},
		{"EC-CTX-020", "covered", ecCtx020},
		{"EC-CTX-021", "covered", ecCtx021},
		{"EC-CTX-022", "covered", ecCtx022},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F2", c.status) {
			t.Run(c.id, func(t *testing.T) {
				t.Parallel()
				c.fn(t)
			})
		}
	}
}

// hot is a meter whose fixed overhead keeps every view above compact_at.
func hot() tokenMeter { return tokenMeter{overhead: 80_000, window: 100_000} }

// fit is a meter with a window large enough for any fixture view at 60 %.
func fit(events []Event) tokenMeter {
	return tokenMeter{window: int64(len(events)) * 600}
}

func failing(err error) func(context.Context, *SummaryRequest) (Reply, error) {
	return func(context.Context, *SummaryRequest) (Reply, error) { return Reply{}, err }
}

// EC-CTX-001: fixed overhead above compact_at makes a summary ineffective;
// the next turn does not pay for another one.
func ecCtx001(t *testing.T) {
	r := newRig(t, hot(), replyWith(validSummary))
	r.store.put("s", bulky(t))
	if res := r.run(t, "s"); res.Outcome != OutcomeIneffective || res.SummaryID == 0 {
		t.Fatalf("first run = %+v", res)
	}
	if res := r.run(t, "s"); res.Outcome != OutcomeCooldown || r.sum.count() != 1 {
		t.Fatalf("second run = %+v after %d calls", res, r.sum.count())
	}
}

// EC-CTX-002: the cooldown ends; one probe runs, and a failed probe closes
// stage 2 again instead of blocking it forever or retrying every turn.
func ecCtx002(t *testing.T) {
	r := newRig(t, hot(), failing(errors.New("overloaded")))
	r.store.put("s", bulky(t))
	r.run(t, "s")
	r.clock.add(DefaultCooldown)
	if res := r.run(t, "s"); res.Outcome != OutcomeFailed || r.sum.count() != 2 {
		t.Fatalf("probe = %+v, calls %d", res, r.sum.count())
	}
	if res := r.run(t, "s"); res.Outcome != OutcomeCooldown || r.sum.count() != 2 {
		t.Fatalf("after failed probe = %+v, calls %d", res, r.sum.count())
	}
}

// EC-CTX-003: a failed first summary installs a cooldown and no fallback
// marker enters the view.
func ecCtx003(t *testing.T) {
	r := newRig(t, hot(), failing(errors.New("boom")))
	r.store.put("s", bulky(t))
	if res := r.run(t, "s"); res.Outcome != OutcomeFailed || res.Cause != "boom" {
		t.Fatalf("run = %+v", res)
	}
	r.clock.add(DefaultCooldown - time.Second)
	if res := r.run(t, "s"); res.Outcome != OutcomeCooldown || r.sum.count() != 1 {
		t.Fatalf("within cooldown = %+v", res)
	}
	assertNoSummary(t, r)
}

func assertNoSummary(t *testing.T, r *rig) {
	t.Helper()
	v := view(t, r)
	for _, m := range v.Messages {
		if strings.Contains(m.Content, "[summary of events") {
			t.Fatalf("summary marker in view: %.80q", m.Content)
		}
	}
	for _, c := range r.store.committed() {
		if c.Summary != nil {
			t.Fatal("a summary was committed")
		}
	}
}

// EC-CTX-004: provider down or empty content aborts stage 2; the stored
// transcript is untouched and the view keeps every message.
func ecCtx004(t *testing.T) {
	for _, reply := range []func(context.Context, *SummaryRequest) (Reply, error){failing(errors.New("dial tcp: refused")), replyWith("")} {
		r := newRig(t, hot(), reply)
		evs := bulky(t)
		r.store.put("s", evs)
		r.run(t, "s")
		v, err := r.c.View(t.Context(), "s")
		if err != nil || len(v.Messages) != messageCount(evs) || len(r.store.events["s"]) < len(evs) {
			t.Fatalf("view = %d messages, %v", len(v.Messages), err)
		}
		assertNoSummary(t, r)
	}
}

func view(t *testing.T, r *rig) View {
	t.Helper()
	v, err := r.c.View(t.Context(), "s")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func messageCount(evs []Event) int {
	items, _ := history(evs)
	return len(items)
}

// EC-CTX-005: repeated overload never wipes the session: after many failed
// probes every message is still in the view.
func ecCtx005(t *testing.T) {
	r := newRig(t, hot(), failing(errors.New("529 overloaded")))
	r.store.put("s", bulky(t))
	for range 6 {
		r.run(t, "s")
		r.clock.add(DefaultCooldown)
	}
	v, err := r.c.View(t.Context(), "s")
	if err != nil || len(v.Messages) != 48 || r.sum.count() != 6 {
		t.Fatalf("view = %d messages after %d calls, %v", len(v.Messages), r.sum.count(), err)
	}
}

// EC-CTX-008: only the newest images are sent; older ones become stubs.
func ecCtx008(t *testing.T) {
	b := transcript{t: t}
	for i := range 4 {
		b.user(fmt.Sprintf("shot %d", i), fmt.Sprintf("%064d", i))
		b.assistant("ok")
	}
	r := newRig(t, fit(b.events), replyWith(validSummary))
	r.store.put("s", b.events)
	r.run(t, "s")
	v := view(t, r)
	var sent int
	for _, m := range v.Messages {
		sent += len(m.Images)
	}
	if sent != keepImageTurns || !strings.Contains(v.Messages[0].Content, "[image 000000000000 omitted]") {
		t.Fatalf("images sent = %d, first = %q", sent, v.Messages[0].Content)
	}
}

// summarised runs one successful summary over a single long agentic turn
// and returns the view.
func summarised(t *testing.T) (*rig, View) {
	t.Helper()
	b := transcript{t: t}
	b.user("the only request: port the parser")
	for i := range 20 {
		b.call("read_file", fmt.Sprintf(`{"path":"f%d.go"}`, i), strings.Repeat("code ", 300))
	}
	r := newRig(t, tokenMeter{overhead: 1000, window: 5_000}, replyWith(validSummary))
	r.store.put("s", b.events)
	if res := r.run(t, "s"); res.Outcome != OutcomeSummarized {
		t.Fatalf("run = %+v", res)
	}
	v, err := r.c.View(t.Context(), "s")
	if err != nil {
		t.Fatal(err)
	}
	return r, v
}

// EC-CTX-009: the request summarised away is restated after the summary.
func ecCtx009(t *testing.T) {
	_, v := summarised(t)
	if v.Messages[1].Role != "user" || v.Messages[1].Content != "the only request: port the parser" {
		t.Fatalf("restated = %+v", v.Messages[1])
	}
}

// EC-CTX-010: the summary is a user message even with no earlier user turn.
func ecCtx010(t *testing.T) {
	_, v := summarised(t)
	if v.Messages[0].Role != "user" || !strings.HasPrefix(v.Messages[0].Content, "[summary of events 2-") {
		t.Fatalf("first message = %+v", v.Messages[0])
	}
}

// EC-CTX-011: the cut follows tool groups: the kept part starts with an
// assistant message and every call keeps its result.
func ecCtx011(t *testing.T) {
	_, v := summarised(t)
	tail := v.Messages[2:]
	if tail[0].Role != "assistant" {
		t.Fatalf("kept part starts with %q", tail[0].Role)
	}
	for i, m := range tail {
		if m.Role == "tool" && tail[i-1].ToolCalls[0].ID != m.ToolCallID {
			t.Fatalf("result %s separated from its call", m.ToolCallID)
		}
	}
}

// EC-CTX-012: one turn larger than the window is cut inside the turn at a
// tool-group boundary instead of leaving nothing to summarise.
func ecCtx012(t *testing.T) {
	r, v := summarised(t)
	sum := v.Summaries[0]
	if sum.FromSeq != 2 || sum.ToSeq < 10 || len(v.Messages) != 2+keepTail || !strings.Contains(r.sum.last.Input, "[seq 2] user") {
		t.Fatalf("summary %d-%d, view %d", sum.FromSeq, sum.ToSeq, len(v.Messages))
	}
}

// EC-CTX-013: every summary block ends with an explicit end marker. The
// payload is always wrapped (SEC18-2), so the wrapper closes just before it.
func ecCtx013(t *testing.T) {
	_, v := summarised(t)
	if !strings.HasSuffix(v.Messages[0].Content, validSummary+`</untrusted>`+"\n[end of summary]") {
		t.Fatalf("summary = %q", v.Messages[0].Content)
	}
}

// EC-CTX-014: summaries and stage-2 state never cross sessions.
func ecCtx014(t *testing.T) {
	r := newRig(t, hot(), failing(errors.New("down")))
	r.store.put("a", bulky(t))
	r.store.put("b", bulky(t))
	r.run(t, "a")
	if res := r.run(t, "b"); res.Outcome != OutcomeFailed {
		t.Fatalf("session b inherited a's cooldown: %+v", res)
	}
	r.sum.reply = replyWith(validSummary)
	r.clock.add(DefaultCooldown)
	r.run(t, "a")
	vb, err := r.c.View(t.Context(), "b")
	if err != nil || len(vb.Summaries) != 0 {
		t.Fatalf("b sees %d summaries, %v", len(vb.Summaries), err)
	}
}

// EC-CTX-015: a summary not smaller than its input is refused; the call is
// still recorded.
func ecCtx015(t *testing.T) {
	b := transcript{t: t}
	for range 6 {
		b.user("hi")
		b.assistant("ok")
	}
	r := newRig(t, tokenMeter{overhead: 900, window: 1000}, replyWith(validSummary))
	r.store.put("s", b.events)
	res := r.run(t, "s")
	if c := r.store.committed(); res.Outcome != OutcomeRejected || len(c) != 1 || c[0].Summary != nil {
		t.Fatalf("run = %+v, commits %+v", res, c)
	}
}

// EC-CTX-016: an interrupted summary call leaves nothing behind.
func ecCtx016(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	r := newRig(t, hot(), func(ctx context.Context, _ *SummaryRequest) (Reply, error) {
		cancel()
		<-ctx.Done()
		return Reply{}, ctx.Err()
	})
	r.store.put("s", bulky(t))
	if _, err := r.c.Run(ctx, "s", ReasonEstimate); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if len(r.store.committed()) != 0 {
		t.Fatal("cancelled summary committed")
	}
	assertNoSummary(t, r)
}

// EC-CTX-018: without provider usage the stored token counts fall back to
// the local estimate.
func ecCtx018(t *testing.T) {
	r := newRig(t, hot(), func(context.Context, *SummaryRequest) (Reply, error) {
		return Reply{Content: validSummary, Call: Call{Model: "m"}}, nil
	})
	r.store.put("s", bulky(t))
	r.run(t, "s")
	c := r.store.committed()
	if len(c) != 1 || c[0].Summary == nil || c[0].Summary.InputTokens <= 0 || c[0].Summary.OutputTokens <= 0 {
		t.Fatalf("commits = %+v", c)
	}
}

// EC-CTX-019: the unread tool round survives the compacted view whole.
func ecCtx019(t *testing.T) {
	b := sampleTranscript(t, 40)
	r := newRig(t, fit(b.events), replyWith(validSummary))
	r.store.put("s", b.events)
	r.run(t, "s")
	v := view(t, r)
	if got := contentOf(t, v.Messages, "p1"); got != strings.Repeat("line\n", 30) {
		t.Fatalf("pending result = %q", got)
	}
}

// EC-CTX-020: short, empty and orphan-only histories never index out of
// range and summarise nothing.
func ecCtx020(t *testing.T) {
	short := transcript{t: t}
	short.user("hi")
	orphans := []Event{{Seq: 1, Kind: event.ToolResult, ToolCallID: "x"}, {Seq: 2, Kind: event.ToolResult, ToolCallID: "y"}}
	for _, evs := range [][]Event{nil, short.events, orphans} {
		r := newRig(t, tokenMeter{overhead: 99, window: 100}, replyWith(validSummary))
		r.store.put("s", evs)
		if res := r.run(t, "s"); res.Outcome != OutcomeNoInput || r.sum.count() != 0 {
			t.Fatalf("run(%d events) = %+v", len(evs), res)
		}
	}
}

// EC-CTX-021: a message appended while the summary is being written stays
// in the view after the boundary.
func ecCtx021(t *testing.T) {
	var r *rig
	r = newRig(t, hot(), func(ctx context.Context, req *SummaryRequest) (Reply, error) {
		r.store.appendEvent("s", &Event{Kind: event.UserMessage, Role: "user", Content: "steer: also fix docs"})
		return replyWith(validSummary)(ctx, req)
	})
	r.store.put("s", bulky(t))
	r.run(t, "s")
	v := view(t, r)
	if last := v.Messages[len(v.Messages)-1]; last.Content != "steer: also fix docs" || len(v.Summaries) != 1 {
		t.Fatalf("last = %+v, summaries %d", last, len(v.Summaries))
	}
}

// EC-CTX-022: a failed commit returns an error and leaves no summary or
// marker behind (ctxdb/compactstore TestCommitAtomic covers the rollback).
func ecCtx022(t *testing.T) {
	r := newRig(t, hot(), replyWith(validSummary))
	r.store.put("s", bulky(t))
	r.store.failCommit = errors.New("disk full")
	if _, err := r.c.Run(t.Context(), "s", ReasonEstimate); err == nil {
		t.Fatal("Run hid a failed commit")
	}
	r.store.failCommit = nil
	assertNoSummary(t, r)
}

// EC-CTX-007: delivery directives (with a value or bare) are removed before
// the summarizer, so a summary can never replay MEDIA: as an attachment.
func ecCtx007(t *testing.T) {
	items := []item{
		{Message: Message{Role: "tool", Content: "found MEDIA:/tmp/foo.png and done"}, seq: 1, tool: "session_search"},
		{Message: Message{Role: "assistant", Content: "here MEDIA:/tmp/bar.png"}, seq: 2},
		{Message: Message{Role: "assistant", Content: "trailing MEDIA:"}, seq: 3},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "shell", Arguments: `{"command":"echo MEDIA:/tmp/baz.png"}`}}}, seq: 4},
	}
	got, _ := renderInput(items)
	if strings.Contains(got, "MEDIA:") || strings.Contains(got, "/tmp/") {
		t.Fatalf("directive reached the summarizer: %q", got)
	}
	for _, want := range []string{"found  and done", "here", "trailing ", "echo "} {
		if !strings.Contains(got, want) {
			t.Fatalf("input lost %q: %q", want, got)
		}
	}
}

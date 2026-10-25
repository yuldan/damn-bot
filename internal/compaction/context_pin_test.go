package compaction

import (
	"strings"
	"testing"
)

// TestPinnedMessageSurvivesCompaction (MEM-025): a pinned transcript position
// is never handed to the summarizer and stays verbatim in the request view,
// because the store loads it back even though it sits before the boundary. The
// unpinned twin is the control: it is summarised away and leaves the view.
func TestPinnedMessageSurvivesCompaction(t *testing.T) {
	t.Parallel()
	const marker = "PINNED-MARKER-KEEP-42"
	for _, pinned := range []bool{true, false} {
		b := transcript{t: t}
		b.user("start the run")
		_, seq := b.call("read_file", `{"path":"keep.txt"}`, marker)
		for range 6 { // push the pinned result well before the kept tail
			b.user(strings.Repeat("chatter ", 200))
			b.call("glob", `{}`, "x")
		}
		r := newRig(t, tokenMeter{window: 1 << 40}, replyWith(validSummary))
		r.store.put("s", b.events)
		if pinned {
			r.store.pins["s"] = []int64{seq}
		}
		if err := r.c.Compact(t.Context(), "s", true); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(r.sum.last.Input, marker); got == pinned {
			t.Errorf("pinned=%v: summarizer input contains the marker = %v, want %v", pinned, got, !pinned)
		}
		v, err := r.c.View(t.Context(), "s")
		if err != nil {
			t.Fatal(err)
		}
		inView := false
		for _, m := range v.Messages {
			if strings.Contains(m.Content, marker) {
				inView = true
			}
		}
		if pinned != inView {
			t.Errorf("pinned=%v: marker in the view = %v, want %v", pinned, inView, pinned)
		}
	}
}

// TestPruneKeepsPinnedContent: a stage-1 rule may not rewrite a pinned
// message; the identical unpinned twin is stubbed by the duplicate rule
// (control).
func TestPruneKeepsPinnedContent(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("payload ", 400)
	newItems := func() []item {
		return []item{
			{Message: Message{Role: "tool", Content: body}, seq: 3, tool: "read_file", args: `{"path":"a"}`},
			{Message: Message{Role: "tool", Content: body}, seq: 10, tool: "read_file", args: `{"path":"a"}`},
		}
	}
	pinned := newItems()
	prune(pinned, []int64{3})
	if pinned[0].Content != body || pinned[0].stubbed {
		t.Errorf("pinned content was rewritten: stubbed=%v len=%d", pinned[0].stubbed, len(pinned[0].Content))
	}
	free := newItems()
	prune(free, nil)
	if free[0].Content == body || !free[0].stubbed {
		t.Errorf("control: unpinned duplicate was not stubbed (stubbed=%v len=%d)", free[0].stubbed, len(free[0].Content))
	}
}

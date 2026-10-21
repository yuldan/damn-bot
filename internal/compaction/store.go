package compaction

import (
	"context"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// Summary is one summaries row (SPEC §4.7, §11.2): an index over the event
// range [FromSeq, ToSeq]; the events themselves stay recallable.
type Summary struct {
	ID             int64
	FromSeq, ToSeq int64
	Content        string // canonical JSON {"goal","decisions","files","open_items","facts"}
	Model          string
	InputTokens    int64
	OutputTokens   int64
	// Tainted marks a summary whose input held untrusted content; it is
	// rendered inside <untrusted> and kept in the summary_ref payload.
	Tainted bool
}

// Snapshot is a session as compaction reads it: every summary in to_seq
// order, the model-visible events after the newest summary's ToSeq, and the
// newest user message at or before that boundary (Task, nil without a
// summary), which the view restates when the summary covered the request
// of the running turn (EC-CTX-009).
type Snapshot struct {
	Summaries []Summary
	Events    []Event
	Task      *Event
}

// Boundary is the newest summary's ToSeq (0 without summaries).
func (s *Snapshot) Boundary() int64 {
	if len(s.Summaries) == 0 {
		return 0
	}
	return s.Summaries[len(s.Summaries)-1].ToSeq
}

// Call is the accounting of one summary LLM call (P11, purpose=compaction).
type Call struct {
	TurnID    string
	Provider  string
	Model     string
	Usage     event.Usage
	Attempts  int
	LatencyMS int64
}

// Commit is one stage-2 write. The call is always recorded; Summary is nil
// when the reply was rejected (SPEC §4.7: invalid → not stored).
type Commit struct {
	Call    Call
	Summary *Summary
}

// Store is the summary persistence seam (E-09, ADR-074); ctxdb/compactstore
// implements it. Load fails with a not-found error for a missing or purging
// session. Commit is one transaction: the llm_call event and its
// usage_ledger row, then, with a Summary, the summary_ref event and the
// summaries row; on error nothing is persisted (EC-CTX-022). It returns the
// summaries id, 0 without a Summary.
type Store interface {
	Load(ctx context.Context, sessionID string) (Snapshot, error)
	Commit(ctx context.Context, sessionID string, c *Commit) (int64, error)
}

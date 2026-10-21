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
// of the running turn (EC-CTX-009). Compacted and LastCall come from the
// session's `compaction` events.
type Snapshot struct {
	Summaries []Summary
	Events    []Event
	Task      *Event
	Compacted bool     // a Run committed: the view is pruned
	LastCall  *Attempt // newest Attempt whose Outcome.Called(), nil without one
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
	Provider  string
	Model     string
	Usage     event.Usage
	Attempts  int
	LatencyMS int64
}

// Outcome is the typed result of one Run, persisted in its Attempt.
type Outcome string

// Outcomes of Run.
const (
	OutcomePruned      Outcome = "pruned"      // stage 1 reached the target (or no window)
	OutcomeSummarized  Outcome = "summarized"  // stage 2 stored a summary that brought the view below compact_at
	OutcomeIneffective Outcome = "ineffective" // stage 2 stored a summary, the view stays at or above compact_at
	OutcomeNoInput     Outcome = "no_input"    // nothing older than the kept tail
	OutcomeCooldown    Outcome = "cooldown"    // stage 2 closed after a failure
	OutcomeFailed      Outcome = "failed"      // the summary call failed
	OutcomeRejected    Outcome = "rejected"    // the reply was invalid or too large
)

// Called reports whether the outcome followed a summary call; the newest
// such Attempt decides the stage-2 cooldown.
func (o Outcome) Called() bool {
	return o == OutcomeSummarized || o == OutcomeIneffective || o == OutcomeFailed || o == OutcomeRejected
}

// Attempt is the persisted verdict of one Run (a `compaction` event): it
// keeps the view pruned and the stage-2 cooldown across Compactor
// instances and processes.
type Attempt struct {
	Outcome Outcome
	Reason  Reason
	AtMS    int64 // Deps.Now of the Run, unix ms
}

// Commit is the single write of one Run. Call is nil when no summary call
// returned a reply; Summary is nil unless the reply was accepted (SPEC
// §4.7: invalid → not stored).
type Commit struct {
	TurnID  string
	Attempt Attempt
	Call    *Call
	Summary *Summary
}

// Store is the summary persistence seam (E-09, ADR-074); ctxdb/compactstore
// implements it. Load fails with a not-found error for a missing or purging
// session. Commit is one transaction: with a Call, the llm_call event and
// its usage_ledger row; with a Summary, the summary_ref event and the
// summaries row; always the `compaction` event of the Attempt. On error
// nothing is persisted (EC-CTX-022). It returns the summaries id, 0
// without a Summary.
type Store interface {
	Load(ctx context.Context, sessionID string) (Snapshot, error)
	Commit(ctx context.Context, sessionID string, c *Commit) (int64, error)
}

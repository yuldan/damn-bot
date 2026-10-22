package compaction

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// DefaultCooldown closes stage 2 after a failed, rejected or ineffective
// summary.
const DefaultCooldown = 10 * time.Minute

// Meter estimates the request a view produces (the assembler's COST-09
// total) and the model context window, both in tokens; the composition root
// implements it with the same assembler the loop uses.
type Meter interface {
	Measure(ctx context.Context, sessionID string, v *View) (estimate, window int64, err error)
}

// Summarizer performs the stage-2 call with purpose=compaction and returns
// its accounting; the composition root implements it over the provider.
type Summarizer interface {
	Summarize(ctx context.Context, req *SummaryRequest) (Reply, error)
}

// Redactor removes secrets from text compaction stores or reports (SPEC
// §14.3); the composition root passes *redact.Redactor.
type Redactor interface {
	String(s string) string
}

// Deps is the explicit dependency set of a Compactor (E-11).
type Deps struct {
	Store      Store
	Summarizer Summarizer
	Meter      Meter
	Redactor   Redactor
	Now        func() time.Time // nil = time.Now
	Cooldown   time.Duration    // 0 = DefaultCooldown
}

// ErrDeps reports a missing dependency.
var ErrDeps = errors.New("compaction: missing dependency")

// View is the compacted history the assembler sends after the system tiers:
// the active summaries as one user message, the restated request when a
// summary covered it, then the messages after the boundary.
type View struct {
	Messages  []Message
	Summaries []Summary // active, oldest first (≤ activeMaxTokens)
	Boundary  int64
	Stats     PruneStats
	Pruned    bool
}

// Tainted reports whether the request carries an active summary built from
// untrusted input. The composition root must taint the turn before it
// evaluates any tool: a summary only restates earlier transcript text, so it
// never authorises an action (SEC18-1; ADR-070, ADR-122).
func (v *View) Tainted() bool {
	for i := range v.Summaries {
		if v.Summaries[i].Tainted {
			return true
		}
	}
	return false
}

// Result reports one Run; Estimate and Window describe the final view.
type Result struct {
	Outcome          Outcome
	Usage            event.Usage // summary call usage, zero without a reply
	Estimate, Window int64
	SummaryID        int64
	Stats            PruneStats
	Cause            string // redacted failure or rejection reason
}

// Compactor runs the two P8 stages for the loop (it satisfies
// loop.Compactor) and renders the compacted view for the assembler. It
// keeps no per-session state: whether a view is pruned and whether stage 2
// is cooling down derive from the session's `compaction` events, so every
// instance and process agrees (EC-CTX-003, EC-CTX-014) and memory does not
// grow with the number of sessions. It is safe for concurrent use.
type Compactor struct {
	deps Deps
}

// NewCompactor validates deps and returns a Compactor holding a copy.
func NewCompactor(deps *Deps) (*Compactor, error) {
	d := *deps
	switch {
	case d.Store == nil:
		return nil, fmt.Errorf("%w: Store", ErrDeps)
	case d.Summarizer == nil:
		return nil, fmt.Errorf("%w: Summarizer", ErrDeps)
	case d.Meter == nil:
		return nil, fmt.Errorf("%w: Meter", ErrDeps)
	case d.Redactor == nil:
		return nil, fmt.Errorf("%w: Redactor", ErrDeps)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	d.Cooldown = cmp.Or(d.Cooldown, DefaultCooldown)
	return &Compactor{deps: d}, nil
}

// Compact is the loop seam. contextLength is true when the provider
// rejected the request as too long (the second SPEC §4.7 trigger): the
// summary stage then runs whatever the estimate says. A failed or rejected
// summary is not an error: the turn goes on with the stage-1 view and the
// loop decides context_overflow.
func (c *Compactor) Compact(ctx context.Context, sessionID string, contextLength bool) error {
	_, err := c.CompactUsage(ctx, sessionID, contextLength)
	return err
}

// CompactUsage is Compact plus the summary call's usage, so the loop can add
// it to the per-turn budget (SEC12-4).
func (c *Compactor) CompactUsage(ctx context.Context, sessionID string, contextLength bool) (event.Usage, error) {
	reason := ReasonEstimate
	if contextLength {
		reason = ReasonContextLength
	}
	res, err := c.Run(ctx, sessionID, reason)
	return res.Usage, err
}

// Run applies stage 1 and at most one summary (SPEC §4.7): for
// ReasonEstimate only while the stage-1 view holds at least 60 % of the
// window, for ReasonContextLength always. Every Run ends with one Commit
// (its Attempt, plus the call and the summary when there are any); from
// then on the session's view stays pruned. Errors are storage, metering
// and cancellation failures; nothing is persisted then (EC-CTX-016/022).
func (c *Compactor) Run(ctx context.Context, sessionID string, reason Reason) (Result, error) {
	snap, err := c.deps.Store.Load(ctx, sessionID)
	if err != nil {
		return Result{}, fmt.Errorf("compaction: %w", err)
	}
	res, err := c.measure(ctx, sessionID, &snap)
	if err != nil {
		return res, err
	}
	cm := Commit{TurnID: lastTurn(snap.Events), Attempt: Attempt{Reason: reason, AtMS: c.deps.Now().UnixMilli()}}
	if reason == ReasonContextLength || atLeast(res.Estimate, res.Window, targetPercent) {
		if serr := c.summarize(ctx, sessionID, &snap, &cm, &res); serr != nil {
			return res, serr
		}
	}
	cm.Attempt.Outcome = res.Outcome
	id, err := c.deps.Store.Commit(ctx, sessionID, &cm)
	if err != nil {
		return res, fmt.Errorf("compaction: %w", err)
	}
	if cm.Summary != nil {
		res.SummaryID = id
	}
	return res, nil
}

// View renders the session's current view: summaries and boundary always,
// stage-1 prune once a Run committed for the session.
func (c *Compactor) View(ctx context.Context, sessionID string) (View, error) {
	snap, err := c.deps.Store.Load(ctx, sessionID)
	if err != nil {
		return View{}, fmt.Errorf("compaction: %w", err)
	}
	return build(&snap, snap.Compacted), nil
}

func (c *Compactor) measure(ctx context.Context, sid string, snap *Snapshot) (Result, error) {
	v := build(snap, true)
	est, win, err := c.deps.Meter.Measure(ctx, sid, &v)
	if err != nil {
		return Result{}, fmt.Errorf("compaction: measure: %w", err)
	}
	return Result{Outcome: OutcomePruned, Estimate: est, Window: win, Stats: v.Stats}, nil
}

// cooling reports whether the newest summary call failed, was rejected or
// was ineffective less than Cooldown before at (EC-CTX-001/003/005); the
// first Run after it is the single probe (EC-CTX-002).
func (c *Compactor) cooling(snap *Snapshot, at int64) bool {
	l := snap.LastCall
	return l != nil && l.Outcome != OutcomeSummarized && at < l.AtMS+c.deps.Cooldown.Milliseconds()
}

// summarize is stage 2: one call over the raw events from the boundary to
// before the kept tail. It fills cm and res; only cancellation is an error.
func (c *Compactor) summarize(ctx context.Context, sid string, snap *Snapshot, cm *Commit, res *Result) error {
	items, _ := history(snap.Events)
	n := cut(items)
	switch {
	case c.cooling(snap, cm.Attempt.AtMS):
		res.Outcome = OutcomeCooldown
		return nil
	case n == 0:
		res.Outcome = OutcomeNoInput
		return nil
	}
	input, tainted := renderInput(items[:n])
	reply, err := c.deps.Summarizer.Summarize(ctx, &SummaryRequest{SessionID: sid, System: systemPrompt, Input: input, MaxTokens: summaryMaxTokens})
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("compaction: summary: %w", cmp.Or(err, ctx.Err()))
	case err != nil:
		res.Outcome, res.Cause = OutcomeFailed, c.deps.Redactor.String(err.Error())
	default:
		sum := &Summary{FromSeq: items[0].seq, ToSeq: items[n].seq - 1, Model: reply.Call.Model, Tainted: tainted}
		c.accept(ctx, sid, snap, cm, &reply, sum, input, res)
	}
	return nil
}

// accept records the call, validates the redacted reply and, when it is
// valid, stores the summary with its measured effect.
func (c *Compactor) accept(ctx context.Context, sid string, snap *Snapshot, cm *Commit, reply *Reply, sum *Summary, input string, res *Result) {
	cm.Call = &reply.Call
	res.Usage = reply.Call.Usage
	inTokens := budget.Count(input).Raw()
	content, err := validate(reply.Content, inTokens, c.deps.Redactor)
	if err != nil {
		res.Outcome, res.Cause = OutcomeRejected, err.Error()
		return
	}
	sum.Content = content
	// Without provider usage the estimates stand in (EC-CTX-018).
	sum.InputTokens = cmp.Or(reply.Call.Usage.Input, inTokens+budget.Count(systemPrompt).Raw())
	sum.OutputTokens = cmp.Or(reply.Call.Usage.Output, budget.Count(reply.Content).Raw())
	cm.Summary = sum
	c.verdict(ctx, sid, snap, sum, res)
}

// verdict measures, in memory, the view the new summary produces so its
// effect is stored with it in one transaction. A view still at or above
// compact_at, or one that cannot be measured, is ineffective and closes
// stage 2 (EC-CTX-001). Events appended meanwhile are read by the next
// View (EC-CTX-021).
func (c *Compactor) verdict(ctx context.Context, sid string, snap *Snapshot, sum *Summary, res *Result) {
	after := Snapshot{Summaries: append(slices.Clone(snap.Summaries), *sum), Task: snap.Task}
	for i := range snap.Events {
		e := &snap.Events[i]
		if e.Seq > sum.ToSeq {
			after.Events = snap.Events[i:]
			break
		}
		if e.Kind == event.UserMessage {
			after.Task = e
		}
	}
	m, err := c.measure(ctx, sid, &after)
	res.Outcome = OutcomeIneffective
	if err != nil {
		res.Cause = c.deps.Redactor.String(err.Error())
		return
	}
	res.Estimate, res.Window, res.Stats = m.Estimate, m.Window, m.Stats
	if !atLeast(m.Estimate, m.Window, DefaultCompactAt) {
		res.Outcome = OutcomeSummarized
	}
}

// lastTurn is the turn of the newest model-visible event ("" without one).
func lastTurn(events []Event) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].TurnID
}

// atLeast reports estimate ≥ pct % of window; an unknown window never
// triggers a paid call.
func atLeast(estimate, window, pct int64) bool {
	if window <= 0 || estimate < 0 {
		return false
	}
	if estimate > maxPercentInput || window > maxPercentInput {
		return estimate >= window/100*pct
	}
	return estimate*100 >= pct*window
}

// build renders a snapshot as the request view.
func build(snap *Snapshot, pruned bool) View {
	items, orphans := history(snap.Events)
	var st PruneStats
	if pruned {
		st = prune(items)
	}
	st.Orphans = orphans
	act := active(snap.Summaries)
	msgs := make([]Message, 0, len(items)+2)
	if len(act) > 0 {
		msgs = append(msgs, Message{Role: "user", Content: renderSummaries(act)})
	}
	if snap.Task != nil && !hasUser(items) {
		msgs = append(msgs, Message{Role: "user", Content: snap.Task.Content, Images: snap.Task.Images})
	}
	return View{Messages: append(msgs, messages(items)...), Summaries: act, Boundary: snap.Boundary(), Stats: st, Pruned: pruned}
}

func hasUser(items []item) bool {
	for i := range items {
		if items[i].Role == "user" {
			return true
		}
	}
	return false
}

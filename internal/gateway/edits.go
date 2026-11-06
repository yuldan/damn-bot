package gateway

import "time"

// EditAction is the verdict of one streamed edit request (GAP-GW-10).
type EditAction uint8

// Edit verdicts.
const (
	// EditDisabled means the adapter has no edit stream (Matrix): the caller
	// sends only the final message.
	EditDisabled EditAction = iota
	// EditCoalesce means the minimum interval has not passed; keep the newer
	// text and try again on the next update.
	EditCoalesce
	// EditAllowed means one edit may be sent now.
	EditAllowed
	// EditNewMessage means the per-message edit budget is spent; send the rest
	// as a new message instead of editing again.
	EditNewMessage
)

// EditStream enforces Caps.MinEditInterval and Caps.MaxEditsPerMessage for one
// reply. It is not safe for concurrent use; a streaming consumer owns one.
type EditStream struct {
	min   time.Duration
	max   int
	now   func() time.Time
	last  time.Time
	edits int
}

// NewEditStream builds the edit gate of one reply. A nil now uses time.Now.
func NewEditStream(caps *Caps, now func() time.Time) *EditStream {
	if now == nil {
		now = time.Now
	}
	return &EditStream{min: caps.MinEditInterval, max: caps.MaxEditsPerMessage, now: now}
}

// Ask reports whether one edit of this reply may be sent now.
func (e *EditStream) Ask() EditAction {
	if e.min <= 0 || e.max <= 0 {
		return EditDisabled
	}
	at := e.now()
	if e.edits >= e.max {
		return EditNewMessage
	}
	if !e.last.IsZero() && at.Sub(e.last) < e.min {
		return EditCoalesce
	}
	e.last = at
	e.edits++
	return EditAllowed
}

// Edits reports how many edits were approved so far.
func (e *EditStream) Edits() int { return e.edits }

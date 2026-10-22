package main

import (
	"context"
	"fmt"
)

// memoryNudgeInterval is the user-turn interval of the memory nudge (MEM-003).
const memoryNudgeInterval = 10

// nudgeFacts prefixes a memory reminder every Nth user turn so the agent is
// prompted to persist durable facts. n counts the user messages already stored,
// so this turn is the (n+1)-th and the nudge lands on turns 10, 20, … (MEM-003
// parity: hermes advances its counter with the turn and fires at the interval).
// The first turn (n == 0) and every turn in between return "".
func nudgeFacts(ctx context.Context, model *runModel) string {
	n, err := model.store.UserTurns(ctx, model.sessionID)
	if err != nil || n%memoryNudgeInterval != memoryNudgeInterval-1 {
		return ""
	}
	return "memory_nudge=consider storing durable facts with the memory tool; "
}

// UserTurns counts the user messages already stored in a session.
func (s *ctxStore) UserTurns(ctx context.Context, sid string) (int64, error) {
	n, err := s.db.Count(ctx, "SELECT count(*) FROM events WHERE session_id = ? AND kind = 'user_message'", sid)
	if err != nil {
		return 0, fmt.Errorf("user turns: %w", err)
	}
	return n, nil
}

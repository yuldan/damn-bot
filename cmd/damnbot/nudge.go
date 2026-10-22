package main

import (
	"context"
	"fmt"
)

// memoryNudgeInterval is the user-turn interval of the memory nudge (MEM-003).
const memoryNudgeInterval = 10

// nudgeFacts prefixes a memory reminder every Nth user turn so the agent is
// prompted to persist durable facts.
func nudgeFacts(ctx context.Context, model *runModel) string {
	n, err := model.store.UserTurns(ctx, model.sessionID)
	if err != nil || n == 0 || n%memoryNudgeInterval != 0 {
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

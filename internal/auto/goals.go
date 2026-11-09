//go:build !minimal

package auto

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Goal is one standing objective judged across turns.
type Goal struct {
	Text     string
	MaxTurns int
	Turns    int
	Done     bool
}

// GoalVerdict is the judge's answer for one turn.
type GoalVerdict struct {
	Achieved bool   `json:"achieved"`
	Reason   string `json:"reason"`
}

const goalsMaxTurns = 20

const goalPrompt = `Judge whether the standing goal is achieved. ` +
	`Answer JSON {"achieved": true|false, "reason": "..."} and nothing else.`

// NewGoal validates a goal. The parity cap is 20 turns.
func NewGoal(text string, maxTurns int) (Goal, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Goal{}, fmt.Errorf("%w: goal text is empty", ErrConfig)
	}
	if maxTurns == 0 {
		maxTurns = goalsMaxTurns
	}
	if maxTurns < 1 || maxTurns > goalsMaxTurns {
		return Goal{}, fmt.Errorf("%w: goal max_turns must be in 1..%d", ErrConfig, goalsMaxTurns)
	}
	return Goal{Text: text, MaxTurns: maxTurns}, nil
}

// JudgeGoal asks the auxiliary judge about g. A disabled feature returns
// [ErrDisabled]; a finished or exhausted goal is not sent to the provider.
func JudgeGoal(ctx context.Context, s *Service, g Goal) (GoalVerdict, error) {
	if !s.Enabled(Goals) {
		return GoalVerdict{}, fmt.Errorf("%w: %s", ErrDisabled, Goals)
	}
	if g.Done || g.Turns >= g.MaxTurns {
		return GoalVerdict{}, nil
	}
	resp, err := s.Call(ctx, Goals, goalPrompt+"\n\nGoal: "+g.Text)
	if err != nil {
		return GoalVerdict{}, err
	}
	raw := strings.TrimSpace(resp.Content)
	var v GoalVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return GoalVerdict{}, fmt.Errorf("auto: %s response: %w", Goals, err)
	}
	return v, nil
}

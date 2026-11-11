//go:build !minimal

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/auto"
)

// goalJudge is the AUTO-014 judge seam. The serve composition supplies the
// auxiliary model; tests supply a fake.
type goalJudge interface {
	Judge(ctx context.Context, g auto.Goal) (auto.GoalVerdict, error)
}

// judgeGoalTurn records one completed turn's judge verdict for sessionID. A
// disabled judge (feature off) or a finished/exhausted goal makes no call. A
// budget refusal leaves the turn count unchanged: the judge never ran.
func judgeGoalTurn(ctx context.Context, j goalJudge, dir, sessionID string) error {
	if j == nil {
		return nil
	}
	path := f8StorePath(dir, "slash-goals.json")
	st, err := f8LoadSlash[f8Goal](path)
	if err != nil {
		return err
	}
	g, ok := st.Sessions[sessionID]
	if !ok || g.Text == "" || g.Achieved || g.Turns >= g.MaxTurns {
		return nil
	}
	verdict, err := j.Judge(ctx, auto.Goal{Text: g.Text, MaxTurns: g.MaxTurns, Turns: g.Turns})
	switch {
	case errors.Is(err, auto.ErrDisabled):
		return nil
	case err != nil:
		return fmt.Errorf("goal judge: %w", err)
	}
	g.Turns++
	g.LastReason = verdict.Reason
	g.LastJudgedAt = time.Now().Unix()
	g.Achieved = verdict.Achieved
	st.Sessions[sessionID] = g
	return f8SaveSlash(path, &st)
}

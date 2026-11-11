//go:build !minimal

package auto

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CuratorInput is the cheap state the daily curator needs.
type CuratorInput struct {
	Now       time.Time
	IdleSince time.Time
	Skills    []string
}

// CuratorResult is a list of retire/merge proposals; applying them is a
// separate quarantine review.
type CuratorResult struct {
	Proposals []string
}

// CuratorState enforces one run per 24 h and only while the session is idle for
// at least two hours. Paused stops due runs until Resume (AUTO-003).
type CuratorState struct {
	last   time.Time
	Paused bool
}

// Pause stops future curator runs without losing the 24 h clock.
func (c *CuratorState) Pause() { c.Paused = true }

// Resume re-enables due runs after Pause.
func (c *CuratorState) Resume() { c.Paused = false }

const curatorIdle = 2 * time.Hour

const curatorPrompt = "Review this skill inventory. Propose at most five retire or merge actions, one per line. " +
	"Answer NONE when nothing should change."

// MaybeCurate runs the curator when due. Disabled features and non-due
// invocations return before the provider is touched.
func (c *CuratorState) MaybeCurate(ctx context.Context, s *Service, in CuratorInput) (*CuratorResult, error) {
	if !s.Enabled(Curator) {
		return nil, fmt.Errorf("%w: %s", ErrDisabled, Curator)
	}
	if c.Paused {
		return nil, nil
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !c.last.IsZero() && now.Sub(c.last) < 24*time.Hour {
		return nil, nil
	}
	if in.IdleSince.IsZero() || now.Sub(in.IdleSince) < curatorIdle {
		return nil, nil
	}
	resp, err := s.Call(ctx, Curator, curatorPrompt+"\n\n"+strings.Join(in.Skills, "\n"))
	if err != nil {
		return nil, err
	}
	c.last = now
	if strings.EqualFold(strings.TrimSpace(resp.Content), "NONE") {
		return &CuratorResult{}, nil
	}
	var out []string
	for line := range strings.SplitSeq(resp.Content, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
		if len(out) == 5 {
			break
		}
	}
	return &CuratorResult{Proposals: out}, nil
}

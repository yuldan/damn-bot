//go:build !minimal

package auto

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ReviewResult is one background review result. Suggestions are quarantine-ready
// text; this package never writes memory or skill stores itself.
type ReviewResult struct {
	Suggestions []string
}

// Reviewer enforces the parity rate: at least 8 completed iterations and at
// most one review per 10 turns. It is owned by one session goroutine.
type Reviewer struct {
	turns int
}

const (
	reviewMinIterations = 8
	reviewEveryTurns    = 10
	reviewPrompt        = "Review the completed turn. Propose at most three durable memories or skills. " +
		"Answer one per line; answer NONE when there is nothing durable."
)

// Turn records one completed turn.
func (r *Reviewer) Turn() { r.turns++ }

// MaybeReview calls the auxiliary model when both thresholds are met. A
// disabled feature and a turn below the threshold are clean no-ops once the
// feature is enabled; the provider is never called by default.
func (r *Reviewer) MaybeReview(ctx context.Context, s *Service, iterations int) (*ReviewResult, error) {
	if !s.Enabled(Review) {
		return nil, fmt.Errorf("%w: %s", ErrDisabled, Review)
	}
	if iterations < reviewMinIterations || r.turns < reviewEveryTurns {
		return nil, nil
	}
	resp, err := s.Call(ctx, Review, reviewPrompt+"\n\nIterations: "+strconv.Itoa(iterations))
	if err != nil {
		return nil, err
	}
	r.turns = 0
	if strings.EqualFold(strings.TrimSpace(resp.Content), "NONE") {
		return &ReviewResult{}, nil
	}
	var out []string
	for line := range strings.SplitSeq(resp.Content, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
		if len(out) == 3 {
			break
		}
	}
	return &ReviewResult{Suggestions: out}, nil
}

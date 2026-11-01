//go:build !minimal

package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

const (
	maxResumes = 2
	resumeTTL  = 30 * time.Second
)

var (
	// ErrCheckpointStale reports a resumed stream that does not continue
	ErrCheckpointStale = errors.New("checkpoint incompatible")
	// ErrResumeExhausted reports that the resume attempt budget or the
	ErrResumeExhausted = errors.New("resume budget exhausted")
)

func (o *OpenAI) observeAttempt(ctx context.Context, rt *Route, br retry.Gate, resp *Response, err error, prev *Response, at time.Time, resumes, n, limit int, r *Request, est int64, billed *int) (*Response, time.Time, int, error) {
	if rerr := br.Record(ctx, retry.ClassifyErr(err)); rerr != nil {
		return prev, at, resumes, fmt.Errorf("%s: circuit: %w", rt.Ref(), rerr)
	}
	if err == nil {
		captureCarriers(resp, rt)
		return prev, at, resumes, nil
	}
	if !resp.responded {
		return prev, at, resumes, err
	}
	*billed++ // the upstream accepted the request: it may have been billed
	next, rerr := o.resumePoint(rt, resp, err, prev, resumes)
	if rerr != nil {
		return prev, at, resumes, rerr
	}
	prev, at, resumes = o.advanceCheckpoint(prev, at, resumes, next)
	if n == limit || !retry.ClassifyErr(err).Retryable() {
		return prev, at, resumes, err
	}
	if derr := o.retryDenied(ctx, rt, o.retryProjection(rt, resp, r, est, *billed)); derr != nil {
		return prev, at, resumes, derr
	}
	return prev, at, resumes, err
}

func (o *OpenAI) resumeReady(prev *Response, at time.Time) error {
	return o.checkpointAge(prev, at)
}

func (o *OpenAI) checkpointAge(prev *Response, at time.Time) error {
	if prev == nil || at.IsZero() || o.now().Sub(at) <= resumeTTL {
		return nil
	}
	return &Error{Class: retry.Unclassified, Err: fmt.Errorf("%w: checkpoint older than %s", ErrResumeExhausted, resumeTTL)}
}

func (o *OpenAI) advanceCheckpoint(prev *Response, at time.Time, resumes int, next *Response) (*Response, time.Time, int) {
	if next == nil {
		return nil, at, resumes
	}
	if prev == nil {
		at = o.now()
	}
	return next, at, resumes + 1
}

func (o *OpenAI) resumePoint(rt *Route, resp *Response, err error, prev *Response, resumes int) (*Response, error) {
	if _, ok := wireOf(rt).(ResumeWire); !ok || !resumeEligible(resp, err) {
		return nil, nil
	}
	if resumes >= maxResumes {
		return nil, &Error{Class: retry.Unclassified, Err: fmt.Errorf("%w: %d resumes", ErrResumeExhausted, resumes)}
	}
	if prev != nil {
		return prev, nil
	}
	cp := *resp
	return &cp, nil
}

func resumeEligible(resp *Response, err error) bool {
	if !retry.ClassifyErr(err).Retryable() || !resp.responded {
		return false
	}
	return resp.Content != "" || resp.ReasoningText != "" || len(resp.ToolCalls) > 0 || len(resp.Reasoning) > 0
}

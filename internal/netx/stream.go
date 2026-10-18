package netx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// Stream watchdog (GAP-REL-07, SELF-HEALING §2).
const (
	ttfbBase     = 25 * time.Second
	ttfbPerChunk = 5 * time.Second // per 32K input tokens
	ttfbChunkTok = 32 << 10
	ttfbMax      = 90 * time.Second
	// StreamIdle is the longest gap between two dispatched events.
	StreamIdle = 60 * time.Second
)

// TTFB returns the time-to-first-event deadline for a request with
// inputTokens prompt tokens: 25 s + 5 s per started 32K tokens, ≤ 90 s.
func TTFB(inputTokens int) time.Duration {
	chunks := (max(inputTokens, 0) + ttfbChunkTok - 1) / ttfbChunkTok
	return min(ttfbBase+time.Duration(chunks)*ttfbPerChunk, ttfbMax)
}

// Stream is an SSE response guarded by a watchdog: the first event must
// arrive within the TTFB deadline and every next one within [StreamIdle].
// Only dispatched events reset the watchdog; comments and keepalives do
// not (EC-PROV-009). Expiry aborts the request and Next returns an error
// wrapping ErrTTFB or ErrIdle (class timeout).
type Stream struct {
	name   string
	body   io.ReadCloser
	p      *SSEParser
	timer  *time.Timer
	idle   time.Duration
	seen   *atomic.Bool // an event was dispatched: expiry is idle, not TTFB
	cause  func() error // context.Cause of the request context
	cancel context.CancelCauseFunc
}

// Stream sends req (built with ctx by the caller or not; its context is
// replaced by a child of ctx) and returns the event stream of a 2xx
// response; a non-2xx status is a *StatusError. The caller must Close the
// stream.
func (c *Client) Stream(ctx context.Context, req *http.Request, ttfb time.Duration) (*Stream, error) {
	sctx, cancel := context.WithCancelCause(ctx)
	seen := new(atomic.Bool)
	timer := time.AfterFunc(ttfb, func() {
		if seen.Load() {
			cancel(&classedError{err: ErrIdle, class: retry.Timeout})
			return
		}
		cancel(&classedError{err: ErrTTFB, class: retry.Timeout})
	})
	body, err := c.body(req.WithContext(sctx))
	if err != nil {
		timer.Stop()
		err = watchdogErr(err, context.Cause(sctx))
		cancel(nil)
		return nil, err
	}
	return &Stream{
		name: c.name, body: body, p: NewSSEParser(body, 0), timer: timer, idle: c.idle, seen: seen,
		cause: func() error { return context.Cause(sctx) }, cancel: cancel,
	}, nil
}

// body is [Client.DoOK] handing ownership of the response body to the caller.
func (c *Client) body(req *http.Request) (io.ReadCloser, error) {
	resp, err := c.DoOK(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Next returns the next event (valid until the following call) or an
// error; io.EOF marks a clean end of stream.
func (s *Stream) Next() (Event, error) {
	ev, err := s.p.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Event{}, io.EOF
		}
		return Event{}, fmt.Errorf("%s stream: %w", s.name, watchdogErr(classify(err), s.cause()))
	}
	s.seen.Store(true)
	s.timer.Reset(s.idle)
	return ev, nil
}

// Close stops the watchdog, aborts the request if still running, and
// closes the body.
func (s *Stream) Close() error {
	s.timer.Stop()
	s.cancel(nil)
	if err := s.body.Close(); err != nil {
		return fmt.Errorf("%s stream close: %w", s.name, err)
	}
	return nil
}

// Retry exposes the server's SSE reconnection delay, if any.
func (s *Stream) Retry() (time.Duration, bool) { return s.p.Retry() }

// watchdogErr prefers the watchdog cause when the request was aborted by
// the TTFB/idle timer, so callers see a timeout rather than a cancel.
func watchdogErr(err, cause error) error {
	if errors.Is(cause, ErrTTFB) || errors.Is(cause, ErrIdle) {
		return fmt.Errorf("%w: %w", cause, err)
	}
	return err
}

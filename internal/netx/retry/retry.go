package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// SPEC P6 / §4.1.1 limits.
const (
	MaxAttempts   = 3                // attempts per model
	MaxRetryAfter = 30 * time.Second // Retry-After honoured up to this, else give up
	Jitter        = 0.2              // ±20 %
)

// backoff returns the base delay after failed attempt n (1-based): 1 s, 4 s.
func backoff(n int) time.Duration {
	if n <= 1 {
		return time.Second
	}
	return 4 * time.Second
}

// ErrExhausted wraps the last error when every attempt failed with a
// retryable class; Class stays retryable so the caller may fall back.
var ErrExhausted = errors.New("retry attempts exhausted")

// ErrRetryAfterTooLong wraps a rate_limited error whose Retry-After exceeds
// [MaxRetryAfter]: retrying this model would block too long (EC-PROV-010).
var ErrRetryAfterTooLong = errors.New("retry-after exceeds 30s")

// Attempt describes one scheduled retry, reported through Policy.Notify
// (SPEC §15: every retry is a WARN with a structured reason).
type Attempt struct {
	N     int           // the failed attempt, 1-based
	Class Class         // its class
	Delay time.Duration // wait before attempt N+1
	Err   error         // its error
}

// Policy configures [Do]. The zero value is the SPEC policy.
type Policy struct {
	// Attempts overrides MaxAttempts for this call; 0 = MaxAttempts.
	Attempts int
	// Rand returns a uniform value in [0,1) for jitter; nil = math/rand/v2.
	Rand func() float64
	// Notify, if set, observes every scheduled retry before the wait.
	Notify func(Attempt)
	// Wait blocks for d or until ctx ends; nil = a real timer. Tests that
	// cannot run inside a synctest bubble (real sockets) inject a fake.
	Wait func(ctx context.Context, d time.Duration) error
}

// Error is the failure returned by [Do]: the last attempt's error with its
// class and the number of attempts made.
type Error struct {
	Class    Class
	Attempts int
	Err      error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s after %d attempt(s): %v", e.Class, e.Attempts, e.Err)
}

// Unwrap exposes the last attempt's error (and ErrExhausted when relevant).
func (e *Error) Unwrap() error { return e.Err }

// RetryClass makes the class visible to [ClassifyErr] up the stack.
func (e *Error) RetryClass() Class { return e.Class }

// Do runs op up to [MaxAttempts] times. A retryable failure waits the
// Retry-After hint (≤ 30 s) or the jittered backoff (1 s, 4 s); any other
// class, a cancelled ctx, or an open circuit returns at once. op receives the
// 1-based attempt number. The result is nil or an *Error.
func Do(ctx context.Context, p Policy, op func(ctx context.Context, attempt int) error) error {
	limit := p.Attempts
	if limit <= 0 {
		limit = MaxAttempts
	}
	for n := 1; ; n++ {
		err := op(ctx, n)
		class := ClassifyErr(err)
		if class == None {
			return nil
		}
		if ctx.Err() != nil {
			// The caller's context ended: never retry, keep its class.
			return &Error{Class: ClassifyErr(context.Cause(ctx)), Attempts: n, Err: err}
		}
		if !class.Retryable() || errors.Is(err, ErrCircuitOpen) {
			return &Error{Class: class, Attempts: n, Err: err}
		}
		if n == limit {
			return &Error{Class: class, Attempts: n, Err: fmt.Errorf("%w: %w", ErrExhausted, err)}
		}
		delay, ok := p.delay(n, err)
		if !ok {
			return &Error{Class: class, Attempts: n, Err: fmt.Errorf("%w: %w", ErrRetryAfterTooLong, err)}
		}
		if p.Notify != nil {
			p.Notify(Attempt{N: n, Class: class, Delay: delay, Err: err})
		}
		w := wait
		if p.Wait != nil {
			w = p.Wait
		}
		if werr := w(ctx, delay); werr != nil {
			return &Error{Class: ClassifyErr(werr), Attempts: n, Err: err}
		}
	}
}

// delay picks the wait after failed attempt n; ok=false when the server
// asks for more than MaxRetryAfter.
func (p Policy) delay(n int, err error) (time.Duration, bool) {
	var ra ServerHint
	if errors.As(err, &ra) && ra.RetryAfter() > 0 {
		d := ra.RetryAfter()
		return d, d <= MaxRetryAfter
	}
	r := rand.Float64
	if p.Rand != nil {
		r = p.Rand
	}
	base := backoff(n)
	// Uniform in [base·(1−J), base·(1+J)).
	return time.Duration(float64(base) * (1 - Jitter + 2*Jitter*r())), true
}

// wait blocks for d or until ctx ends (returns its cause).
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("retry wait: %w", context.Cause(ctx))
	}
}

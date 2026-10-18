package retry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// classErr is a test error with a fixed class and optional Retry-After.
type classErr struct {
	c  Class
	ra time.Duration
}

func (e classErr) Error() string             { return "fake " + e.c.String() }
func (e classErr) RetryClass() Class         { return e.c }
func (e classErr) RetryAfter() time.Duration { return e.ra }

func allClasses() []Class {
	out := make([]Class, 0, Unclassified)
	for c := RateLimited; c <= Unclassified; c++ {
		out = append(out, c)
	}
	return out
}

// run executes Do in a synctest bubble with a constant jitter draw r and
// returns the error, the number of op calls, and the fake time elapsed.
func run(t *testing.T, r float64, op func(ctx context.Context, n int) error) (calls int, took time.Duration, err error) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		err = Do(t.Context(), Policy{Rand: func() float64 { return r }}, func(ctx context.Context, n int) error {
			calls++
			if n != calls {
				t.Errorf("attempt %d, want %d", n, calls)
			}
			return op(ctx, n)
		})
		took = time.Since(start)
	})
	return calls, took, err
}

// TestRetryMatrix (SPEC P6, §4.1.1): every class × always-failing op →
// attempts and total wait per the policy (3 attempts, 1 s/4 s ±20 %).
func TestRetryMatrix(t *testing.T) {
	t.Parallel()
	for _, c := range allClasses() {
		for _, r := range []float64{0, 0.5, 0.999999} {
			t.Run(fmt.Sprintf("%s/jitter=%g", c, r), func(t *testing.T) { checkMatrix(t, c, r) })
		}
	}
}

func checkMatrix(t *testing.T, c Class, r float64) {
	t.Helper()
	calls, took, err := run(t, r, func(context.Context, int) error { return classErr{c: c} })
	wantCalls, wantWait := 1, time.Duration(0)
	if c.Retryable() {
		wantCalls = MaxAttempts
		f := 1 - Jitter + 2*Jitter*r
		wantWait = time.Duration(float64(time.Second)*f) + time.Duration(float64(4*time.Second)*f)
	}
	var re *Error
	if !errors.As(err, &re) || re.Class != c || re.Attempts != wantCalls || calls != wantCalls {
		t.Fatalf("err=%v calls=%d, want class %s after %d", err, calls, c, wantCalls)
	}
	if took != wantWait || took > 6*time.Second || c.Retryable() && took < 4*time.Second {
		t.Errorf("waited %v, want %v (1 s/4 s ±20 %%)", took, wantWait)
	}
	if c.Retryable() != errors.Is(err, ErrExhausted) {
		t.Errorf("ErrExhausted = %v, want %v", errors.Is(err, ErrExhausted), c.Retryable())
	}
	if ClassifyErr(err) != c {
		t.Errorf("ClassifyErr(Do error) = %s, want %s", ClassifyErr(err), c)
	}
}

func TestRetryScenarios(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		errs      []error // per attempt; nil = success
		wantCalls int
		wantWait  time.Duration
		wantErr   error // nil = success
	}{
		{name: "success first", errs: []error{nil}, wantCalls: 1},
		{name: "success second", errs: []error{classErr{c: Server}, nil}, wantCalls: 2, wantWait: time.Second},
		{name: "retry-after replaces backoff", errs: []error{classErr{c: RateLimited, ra: 10 * time.Second}, classErr{c: RateLimited, ra: 30 * time.Second}, nil}, wantCalls: 3, wantWait: 40 * time.Second},
		{name: "retry-after too long", errs: []error{classErr{c: RateLimited, ra: 31 * time.Second}}, wantCalls: 1, wantErr: ErrRetryAfterTooLong},
		{name: "circuit open", errs: []error{ErrCircuitOpen}, wantCalls: 1, wantErr: ErrCircuitOpen},
		{name: "retryable then fatal", errs: []error{classErr{c: Network}, classErr{c: Auth}}, wantCalls: 2, wantWait: time.Second, wantErr: classErr{c: Auth}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, took, err := run(t, 0.5, func(_ context.Context, n int) error { return tc.errs[n-1] })
			if calls != tc.wantCalls || took != tc.wantWait {
				t.Errorf("calls=%d wait=%v, want %d %v", calls, took, tc.wantCalls, tc.wantWait)
			}
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestRetryInjectedWait: Policy.Wait replaces the timer and its error ends
// the loop with the error's class.
func TestRetryInjectedWait(t *testing.T) {
	t.Parallel()
	var waits []time.Duration
	p := Policy{Rand: func() float64 { return 0.5 }, Wait: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) == 2 {
			return context.Canceled
		}
		return nil
	}}
	err := Do(t.Context(), p, func(context.Context, int) error { return classErr{c: Server} })
	if ClassifyErr(err) != Cancelled || len(waits) != 2 || waits[0] != time.Second || waits[1] != 4*time.Second {
		t.Errorf("err=%v waits=%v", err, waits)
	}
}

// TestRetryCancel: a user cancel during the wait stops at once as cancelled;
// a parent deadline is a timeout; neither retries.
func TestRetryCancel(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		var notified []Attempt
		p := Policy{Notify: func(a Attempt) { notified = append(notified, a); cancel() }}
		err := Do(ctx, p, func(context.Context, int) error { calls++; return classErr{c: Overloaded} })
		if ClassifyErr(err) != Cancelled || calls != 1 || len(notified) != 1 || notified[0].Class != Overloaded {
			t.Errorf("err=%v calls=%d notified=%v", err, calls, notified)
		}
		if d := notified[0].Delay; d < 800*time.Millisecond || d >= 1200*time.Millisecond {
			t.Errorf("first delay %v outside 1 s ±20 %%", d)
		}

		dctx, dcancel := context.WithTimeout(t.Context(), time.Second)
		defer dcancel()
		calls = 0
		err = Do(dctx, Policy{}, func(ctx context.Context, _ int) error {
			calls++
			<-ctx.Done()
			return ctx.Err()
		})
		if ClassifyErr(err) != Timeout || calls != 1 {
			t.Errorf("deadline: err=%v calls=%d", err, calls)
		}
	})
}

func TestClassifyStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		code   string
		want   Class
	}{
		{200, "", None},
		{204, "", None},
		{304, "", None},
		{429, "", RateLimited},
		{429, "invalid_api_key", RateLimited},
		{503, "", Overloaded},
		{529, "", Overloaded},
		{500, "", Server},
		{502, "", Server},
		{504, "", Server},
		{408, "", Timeout},
		{401, "", Auth},
		{403, "", Auth},
		{400, CodeContextLengthExceeded, ContextLength},
		{413, CodeStringAboveMaxLength, ContextLength},
		{400, "", BadRequest},
		{413, "", BadRequest},
		{404, "", BadRequest},
		{422, "", BadRequest},
		{404, CodeContextLengthExceeded, BadRequest},
		{501, "", BadRequest},
		{409, "", BadRequest},
	}
	for _, c := range cases {
		if got := ClassifyStatus(c.status, c.code); got != c.want {
			t.Errorf("ClassifyStatus(%d, %q) = %s, want %s", c.status, c.code, got, c.want)
		}
	}
}

func TestClassifyErr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want Class
	}{
		{nil, None},
		{classErr{c: StreamBroken}, StreamBroken},
		{fmt.Errorf("wrapped: %w", classErr{c: Auth}), Auth},
		{context.Canceled, Cancelled},
		{context.DeadlineExceeded, Timeout},
		{os.ErrDeadlineExceeded, Timeout},
		{&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, Network},
		{fmt.Errorf("read: %w", syscall.ECONNRESET), Network},
		{io.ErrUnexpectedEOF, Network},
		{ErrCircuitOpen, Overloaded},
		{io.EOF, Unclassified},
		{errors.New("x509: unknown authority"), Unclassified},
	}
	for _, c := range cases {
		if got := ClassifyErr(c.err); got != c.want {
			t.Errorf("ClassifyErr(%v) = %s, want %s", c.err, got, c.want)
		}
	}
	for c := None; c <= Unclassified+1; c++ {
		if c.String() == "" {
			t.Errorf("class %d without name", c)
		}
	}
}

// TestBreaker (SPEC §4.1): 5 retryable failures / 60 s → open 30 s → one
// half-open probe.
func TestBreaker(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var b Breaker
		breakerFail(t, &b, BreakerFailures-1)
		advance(BreakerWindow + time.Second) // old failures leave the window
		breakerFail(t, &b, BreakerFailures-1)
		b.Record(Auth) // non-retryable: neutral while closed
		wantState(t, &b, Closed)
		breakerFail(t, &b, 1)
		wantState(t, &b, Open)
		if !errors.Is(b.Allow(), ErrCircuitOpen) {
			t.Fatal("open circuit allowed a request")
		}
		advance(BreakerOpen)
		if err := b.Allow(); err != nil {
			t.Fatalf("after 30 s: %v, want one probe", err)
		}
		wantState(t, &b, HalfOpen)
		if !errors.Is(b.Allow(), ErrCircuitOpen) {
			t.Fatal("second caller during probe allowed")
		}
		b.Record(Timeout) // probe failed → open again
		wantState(t, &b, Open)
		advance(BreakerOpen)
		if err := b.Allow(); err != nil {
			t.Fatal(err)
		}
		b.Record(None)
		wantState(t, &b, Closed)
		breakerFail(t, &b, BreakerFailures-1) // counters were reset
		wantState(t, &b, Closed)
	})
}

func breakerFail(t *testing.T, b *Breaker, n int) {
	t.Helper()
	for range n {
		if err := b.Allow(); err != nil {
			t.Fatalf("Allow in state %d: %v", b.State(), err)
		}
		b.Record(Server)
	}
}

func wantState(t *testing.T, b *Breaker, s State) {
	t.Helper()
	if got := b.State(); got != s {
		t.Fatalf("state %d, want %d", got, s)
	}
}

// TestEdgeCases (EDGE-CASES-hermes.md, ADR-077/080): entries owned by retry.
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(t *testing.T)
	}{
		// Rate limit wins over auth wording in the body: the class comes from the status.
		{"EC-PROV-029", func(t *testing.T) {
			if c := ClassifyStatus(http.StatusTooManyRequests, "invalid_api_key"); c != RateLimited {
				t.Errorf("429 with auth code = %s", c)
			}
		}},
		// One retry owner with bounded total time: worst case 3 attempts and
		// ≤ 2 × 30 s of waiting, no stacked SDK/app retries.
		{"EC-PROV-010", func(t *testing.T) {
			calls, took, err := run(t, 0.999999, func(context.Context, int) error {
				return classErr{c: RateLimited, ra: MaxRetryAfter}
			})
			if calls != MaxAttempts || took != 2*MaxRetryAfter || !errors.Is(err, ErrExhausted) {
				t.Errorf("calls=%d wait=%v err=%v", calls, took, err)
			}
		}},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// advance moves the synctest fake clock by d (E-72: no time.Sleep in tests).
func advance(d time.Duration) { <-time.After(d) }

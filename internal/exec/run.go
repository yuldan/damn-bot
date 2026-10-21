package exec

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/redact"
)

// Tunables of the run loop.
const (
	readChunk = 32 << 10
	// drainAfterExit bounds reading after the tree is gone (an escaped writer cannot hold the call open).
	drainAfterExit = 250 * time.Millisecond
	// maxTasks bounds the runner's goroutines (E-27): reader, waiter, stdin writer.
	maxTasks = 3 * 256
	// closeWait bounds Runner.Close when a task cannot finish.
	closeWait = 10 * time.Second
)

// errCleanup marks cleanup failures; the result is still valid (the error reports what was not confirmed).
var errCleanup = errors.New("exec: cleanup")

// errCloseTimeout reports that Close gave up waiting for a runner task.
var errCloseTimeout = errors.New("exec: close timeout")

// Runner runs commands on the local backend; Close kills every live tree and waits for its goroutines.
type Runner struct {
	g       *boundary.Group
	prompts []promptRule
	stop    chan struct{} // closed by Close: live calls are cancelled

	mu     sync.Mutex
	live   map[string]chan struct{} // exec id → closed when the tree is dead
	closed bool
	// red redacts detected prompt lines (EX45-1): Prompt goes to the model
	// and UI, so it never carries a known secret.
	red *redact.Redactor
	// closeTask is the retained Close waiter; a later Close waits on it.
	closeTask *boundary.Task
}

// New returns a Runner whose goroutines stop when ctx is canceled.
func New(ctx context.Context) *Runner {
	g, _ := boundary.WithContext(ctx) // members never fail; ctx unused by them
	g.SetLimit(maxTasks)
	return &Runner{
		g: g, prompts: newPromptRules(), stop: make(chan struct{}),
		live: map[string]chan struct{}{}, red: redact.New(redact.Options{}),
	}
}

// SetRedactor installs the session's redactor (built-in patterns plus its
// known secrets) for detected prompt lines. Call it before the first Run.
func (r *Runner) SetRedactor(red *redact.Redactor) {
	if red == nil {
		red = redact.New(redact.Options{})
	}
	r.mu.Lock()
	r.red = red
	r.mu.Unlock()
}

// redactor returns the current prompt redactor.
func (r *Runner) redactor() *redact.Redactor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.red
}

// Close is [Runner.CloseContext] with a background context.
func (r *Runner) Close() error { return r.CloseContext(context.Background()) }

// CloseContext kills still-running trees and waits, bounded by closeWait (a stuck task must not hang shutdown; REVIEW-F1-SEC5 EX5-2).
// The context scopes the shutdown goroutine; the wait stays bounded by closeWait.
func (r *Runner) CloseContext(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.stop)
	}
	task := r.closeTask
	if task == nil {
		task = boundary.Go(ctx, "exec.close", func(context.Context) error {
			return r.g.Wait()
		})
		r.closeTask = task
	}
	r.mu.Unlock()
	timer := time.NewTimer(closeWait)
	defer timer.Stop()
	select {
	case <-task.Done():
		if err := task.Wait(); err != nil {
			return fmt.Errorf("exec: close: %w", err)
		}
		return nil
	case <-timer.C:
		// The task stays on the Runner: a later Close waits for it instead
		// of abandoning a goroutine (REVIEW-F1-SEC5 EX5-16).
		return fmt.Errorf("exec: close: %w", errCloseTimeout)
	}
}

// Live reports exec ids of trees returned as still_running and not yet confirmed dead.
func (r *Runner) Live() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.live))
	for id := range r.live {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// call is the state of one Run.
type call struct {
	r     *Runner
	s     Spec
	p     *osProc
	id    string
	start time.Time
	out   *capture
	read  chan struct{}
	exit  chan exit
	ex    exit
	gone  bool // leader reaped
	// lastOut is the time of the last output chunk (UnixNano).
	lastOut atomic.Int64
	// more, if set, gets a non-blocking signal after each output chunk
	// (persistent sessions wait on it for their sentinel).
	more chan struct{}
	// in, if set, is the child's stdin read end (pipe sessions); spawn
	// closes it after the start.
	in *os.File
	// red redacts a detected prompt line (EX45-1).
	red *redact.Redactor
	// inW is the stdin write end; finish closes it so a blocked writer cannot hold Close (REVIEW-F1-SEC5, #40).
	inW *os.File

	mu   sync.Mutex
	errs error // wait and cleanup failures, returned by Run
}

// note records a failure (nil is ignored).
func (c *call) note(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.errs = errors.Join(c.errs, err)
	c.mu.Unlock()
}

func (c *call) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errs == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", errCleanup, c.errs)
}

// Run executes s and returns once the tree is dead, or 1 s after ctx is canceled (ADR-072).
// A spawn failure is StatusSpawnFailed with the cause in the error.
func (r *Runner) Run(ctx context.Context, spec *Spec) (Result, error) {
	s := *spec
	if err := s.normalize(); err != nil {
		return Result{}, err
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return Result{}, ErrClosed
	}
	c := &call{
		r: r, s: s, id: newExecID(), out: newCapture(), red: r.redactor(),
		read: make(chan struct{}), exit: make(chan exit, 1),
	}
	c.s.Env = withMarker(s.Env, c.id)
	res := Result{ExecID: c.id, Runner: RunnerPipe}
	if s.PTY {
		res.Runner = RunnerPTY
	}
	c.start = time.Now()
	if err := c.spawn(ctx); err != nil {
		res.Status, res.Duration = StatusSpawnFailed, time.Since(c.start)
		return res, err
	}
	c.loop(ctx, &res)
	return res, c.err()
}

func (c *call) spawn(ctx context.Context) error {
	in, inW := c.in, (*os.File)(nil) // c.in: a session's own input pipe
	if in == nil && c.s.Stdin != nil {
		var err error
		if in, inW, err = os.Pipe(); err != nil {
			return fmt.Errorf("exec: stdin pipe: %w", err)
		}
	}
	p, err := start(&c.s, in)
	if in != nil {
		c.note(in.Close())
	}
	if err != nil {
		if inW != nil {
			err = errors.Join(err, inW.Close())
		}
		return err
	}
	c.p = p
	c.inW = inW
	gctx := context.WithoutCancel(ctx) // tasks end with the tree, not ctx
	if inW != nil {
		data := c.s.Stdin
		c.r.g.Go(gctx, "exec.stdin", func(context.Context) error {
			if _, err := inW.Write(data); err != nil && !isBrokenPipe(err) {
				c.note(ignoreClosed(fmt.Errorf("exec: stdin: %w", err)))
			}
			c.note(ignoreClosed(inW.Close()))
			return nil
		})
	}
	c.r.g.Go(gctx, "exec.read", c.readLoop)
	c.r.g.Go(gctx, "exec.wait", func(context.Context) error {
		ex, err := p.wait()
		c.note(err)
		c.exit <- ex
		return nil
	})
	return nil
}

// ignoreClosed drops os.ErrClosed (a descriptor closed by finish).
func ignoreClosed(err error) error {
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func (c *call) readLoop(context.Context) error {
	defer close(c.read)
	buf := make([]byte, readChunk)
	for {
		n, err := c.p.output().Read(buf)
		if n > 0 {
			c.lastOut.Store(time.Now().UnixNano())
			c.out.write(buf[:n])
			if c.s.OnOutput != nil {
				c.s.OnOutput(buf[:n])
			}
			if c.more != nil {
				select {
				case c.more <- struct{}{}:
				default:
				}
			}
		}
		if err != nil {
			return nil // EOF, EIO (PTY hang-up) or closed by finish
		}
	}
}

func (c *call) loop(ctx context.Context, res *Result) {
	timeout := time.NewTimer(c.s.Timeout)
	defer timeout.Stop()
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	w := watch{idle: idleTracker{since: c.start}, nextSample: c.start.Add(idleSampleEvery)}
	for done := false; !done; {
		select {
		case c.ex = <-c.exit:
			c.gone = true
			res.Status = StatusExited
			if c.ex.signal != "" {
				res.Status = StatusSignaled
			}
			done = true
		case <-timeout.C:
			c.killTree(c.s.KillGrace)
			res.Status, done = StatusTimeout, true
		case now := <-tick.C:
			if st := c.check(now, &w, res); st != "" {
				c.killTree(c.s.KillGrace)
				res.Status, done = st, true
			}
		case <-ctx.Done():
			c.cancelled(ctx, res)
			return
		case <-c.r.stop:
			c.cancelled(ctx, res)
			return
		}
	}
	c.finish()
	c.result(res)
}

// watchEvery paces prompt and idle checks.
const watchEvery = 100 * time.Millisecond

// watch is the prompt/idle state of one call.
type watch struct {
	idle       idleTracker
	sampler    treeSampler
	nextSample time.Time
	waiting    bool // Linux wchan: blocked on terminal/pipe input
}

// prompt reports a detected prompt of a PTY or piped-stdin call (§A.6).
func (c *call) prompt(silent time.Duration, waiting bool) (kind, line string) {
	if !c.s.PTY && c.s.Stdin == nil || silent < promptSilence {
		return "", ""
	}
	line = lastLine(c.out.tail(promptTail))
	kind = promptKind(c.r.prompts, line)
	if kind == "" && silent >= wchanSilence && waiting {
		kind = InputText
	}
	return kind, line
}

// check returns waiting_for_input when a prompt is detected (§A.6: PTY or
// piped stdin, silence ≥ 1.5 s and a prompt-like last line, or Linux wchan
// after 5 s) or idle_timeout (§A.4), else "".
func (c *call) check(now time.Time, w *watch, res *Result) Status {
	last := time.Unix(0, c.lastOut.Load())
	if last.Before(c.start) {
		last = c.start
	}
	silent := now.Sub(last)
	if kind, line := c.prompt(silent, w.waiting); kind != "" {
		res.Prompt, res.InputKind = sanitizePrompt(c.red, line), kind
		return StatusWaitingForInput
	}
	if now.Before(w.nextSample) {
		return ""
	}
	w.nextSample = now.Add(idleSampleEvery)
	w.waiting = silent >= wchanSilence && treeWaitsInput(c.id, c.s.PTY)
	if c.s.IdleTimeout <= 0 {
		return ""
	}
	cpu, io, ok := w.sampler.sample(c.p, c.id)
	if w.idle.observe(now, last, cpu, io, ok, c.s.IdleTimeout) {
		return StatusIdleTimeout
	}
	return ""
}

// cancelled runs the kill order with a 1 s result deadline (REL-06); the tree is confirmed dead or returned still_running (kill continues, Result.Done).
func (c *call) cancelled(ctx context.Context, res *Result) {
	deadline := time.Now().Add(CancelDeadline)
	c.note(c.p.terminate())
	if c.waitDead(deadline) {
		c.finish()
		res.Status = StatusCancelledConfirmed
		c.result(res)
		return
	}
	done := make(chan struct{})
	c.r.mu.Lock()
	c.r.live[c.id] = done
	c.r.mu.Unlock()
	c.r.g.Go(context.WithoutCancel(ctx), "exec.reap", func(context.Context) error {
		c.killTree(c.s.KillGrace - CancelDeadline)
		c.finish()
		c.r.mu.Lock()
		delete(c.r.live, c.id)
		c.r.mu.Unlock()
		close(done)
		return nil
	})
	res.Status, res.Done = StatusStillRunning, done
	res.Duration = time.Since(c.start)
}

// killTree is the kill order of §A.4: TERM to the tree, up to grace, then KILL.
func (c *call) killTree(grace time.Duration) {
	c.note(c.p.terminate())
	if !c.waitDead(time.Now().Add(grace)) {
		c.note(c.p.kill())
	}
}

// waitDead reaps the leader and polls the tree until it is gone or the deadline passes.
func (c *call) waitDead(deadline time.Time) bool {
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		if c.gone && !c.p.treeAlive() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case c.ex = <-c.exit:
			c.gone = true
		case <-tick.C:
		}
	}
}

// finish kills stragglers and setsid escapees, reaps the leader and drains the output.
func (c *call) finish() {
	// Release a stdin writer blocked on a pipe held by an escaped process (REVIEW-F1-SEC5, #40).
	if c.inW != nil {
		c.note(ignoreClosed(c.inW.Close()))
	}
	// A live tree may still be forking or exec'ing an escapee: the marker
	// sweep then needs a settle scan (see sweep).
	hadTree := c.p.treeAlive()
	c.note(c.p.kill())
	c.sweep(hadTree)
	if !c.gone {
		c.ex = <-c.exit
		c.gone = true
	}
	c.note(c.p.closeConsole()) // ConPTY: lets the output reach EOF while drained
	drain := time.NewTimer(drainAfterExit)
	select {
	case <-c.read:
	case <-drain.C:
	}
	drain.Stop()
	c.note(ignoreClosed(c.p.closeOutput()))
	<-c.read
	c.note(c.p.release())
}

// sweepWait bounds waiting for killed marker processes to disappear (SIGKILL
// is asynchronous; P24.1 asserts right after Run).
const sweepWait = time.Second

// sweepQuiet is the clean observation required after the last kill: two
// observations, at least pollEvery apart. A process inside execve is
// invisible to the marker scan — Linux reports an empty /proc/<pid>/environ
// and macOS fails kern.procargs2 — so one clean scan does not prove the tree
// is gone while an escapee may still be exec'ing (ADR-105 butir 10). Two
// scans also outlast the delivery of a SIGKILL on a loaded runner, where each
// scan itself takes longer than the window.
const sweepQuiet = 2 * pollEvery

// sweep kills every process carrying this call's marker (§A.4) with the
// confirmation of killMarked: one clean scan does not prove the tree is gone
// — an escapee between two execve calls reports an empty environ — so the
// sweep waits for a quiet window (sweepQuiet) after the last kill and
// reports processes still visible at sweepWait as a cleanup failure. settle
// covers a tree that was alive at kill time; a call with no descendants
// keeps the single-scan fast path (P24.3 spawn budget).
func (c *call) sweep(settle bool) {
	id := []byte(c.id)
	left, _, err := killMarked(func(v []byte) bool { return bytes.Equal(v, id) }, settle)
	c.note(err)
	if len(left) > 0 {
		c.note(fmt.Errorf("exec: sweep: %d marked processes survived", len(left)))
	}
}

// killMarked kills every process accepted by match and returns once a scan
// sees none for sweepQuiet, bounded by sweepWait. It returns the pids still
// visible at the bound (empty when the sweep confirmed them gone) and the
// number of kills it issued. settle requires the quiet window even when the
// first scan is clean, for a caller that had a tree at kill time; a scan
// that finds (and kills) processes requires it in any case, so a caller
// never returns straight from a kill without observing its effect.
func killMarked(match func([]byte) bool, settle bool) (left []int, killed int, err error) {
	deadline := time.Now().Add(sweepWait)
	var quietSince time.Time
	seen := make(map[int]bool)
	for {
		pids, serr := markerPids(match)
		err = errors.Join(err, serr)
		for _, pid := range pids {
			if kerr := killPid(pid); kerr != nil {
				err = errors.Join(err, kerr)
				continue
			}
			if !seen[pid] {
				seen[pid] = true
				killed++
			}
		}
		now := time.Now()
		switch {
		case len(pids) > 0:
			settle, quietSince = true, time.Time{}
		case !settle:
			return nil, killed, err // no tree and nothing found: single scan
		case quietSince.IsZero():
			quietSince = now
		}
		if !quietSince.IsZero() && now.Sub(quietSince) >= sweepQuiet {
			return nil, killed, err
		}
		if !now.Before(deadline) {
			return pids, killed, err
		}
		// A timer, not time.Sleep: the clock rule of E-30/E-72.
		pause := time.NewTimer(pollEvery)
		<-pause.C
	}
}

func (c *call) result(res *Result) {
	res.Duration = time.Since(c.start)
	res.Output, res.Truncated, res.BytesRaw = c.out.bytes()
	res.Signal = c.ex.signal
	if res.Status == StatusExited {
		code := c.ex.code
		res.ExitCode = &code
	}
}

// newExecID is "<owner pid>.<random>"; the owner pid tells a dead damnbot's trees from a live one's.
func newExecID() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand never fails on supported platforms
	return strconv.Itoa(os.Getpid()) + "." + hex.EncodeToString(b[:])
}

// withMarker returns env with MarkerEnv=id and DAMNBOT=1 (hard values, §A.6), replacing any inherited ones.
func withMarker(env []string, id string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if strings.HasPrefix(kv, MarkerEnv+"=") || strings.HasPrefix(kv, "DAMNBOT=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, MarkerEnv+"="+id, "DAMNBOT=1")
}

func (s *Spec) normalize() error {
	if len(s.Argv) == 0 || s.Argv[0] == "" {
		return errors.Join(ErrSpec, errors.New("empty argv"))
	}
	if s.Timeout <= 0 {
		s.Timeout = DefaultTimeout
	}
	if s.Timeout > MaxTimeout {
		return errors.Join(ErrSpec, errors.New("timeout above 600 s"))
	}
	if s.IdleTimeout == 0 {
		s.IdleTimeout = DefaultIdleTimeout
	}
	if s.KillGrace == 0 {
		s.KillGrace = DefaultKillGrace
	}
	s.KillGrace = min(max(s.KillGrace, MinKillGrace), MaxKillGrace)
	return nil
}

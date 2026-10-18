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
	"time"

	"github.com/damnbot/damnbot/internal/boundary"
)

// Tunables of the run loop.
const (
	readChunk = 32 << 10
	// drainAfterExit bounds reading output still buffered after the tree
	// is gone; a writer outside the tree (marker removed by `env -i` and
	// setsid) cannot hold the call open.
	drainAfterExit = 250 * time.Millisecond
	// sweepRounds re-scans while marker processes keep appearing (forks
	// racing the kill).
	sweepRounds = 5
	// maxTasks bounds the runner's goroutines (E-27): reader, waiter and
	// stdin writer per live call.
	maxTasks = 3 * 256
)

// errCleanup marks failures of the kill order or descriptor cleanup; the
// result is still valid, the error reports what could not be confirmed.
var errCleanup = errors.New("exec: cleanup")

// Runner runs commands on the local backend. Its goroutines belong to one
// boundary group; Close kills every live tree and waits for them.
type Runner struct {
	g    *boundary.Group
	stop chan struct{} // closed by Close: live calls are cancelled

	mu     sync.Mutex
	live   map[string]chan struct{} // exec id → closed when the tree is dead
	closed bool
}

// New returns a Runner whose goroutines stop when ctx is canceled.
func New(ctx context.Context) *Runner {
	g, _ := boundary.WithContext(ctx) // members never fail; ctx unused by them
	g.SetLimit(maxTasks)
	return &Runner{g: g, stop: make(chan struct{}), live: map[string]chan struct{}{}}
}

// Close kills still-running trees and waits for the runner's goroutines.
func (r *Runner) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.stop)
	}
	r.mu.Unlock()
	if err := r.g.Wait(); err != nil {
		return fmt.Errorf("exec: close: %w", err)
	}
	return nil
}

// Live reports exec ids of trees returned as still_running and not yet
// confirmed dead.
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

// Run executes s and returns once the tree is dead, or 1 s after ctx is
// canceled (cancelled_confirmed / still_running, ADR-072). A spawn failure
// is Result{Status: StatusSpawnFailed} with the cause in the error.
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
		r: r, s: s, id: newExecID(), out: newCapture(),
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
	var in, inW *os.File
	if c.s.Stdin != nil {
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
	gctx := context.WithoutCancel(ctx) // tasks end with the tree, not ctx
	if inW != nil {
		data := c.s.Stdin
		c.r.g.Go(gctx, "exec.stdin", func(context.Context) error {
			if _, err := inW.Write(data); err != nil && !isBrokenPipe(err) {
				c.note(fmt.Errorf("exec: stdin: %w", err))
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
			c.out.write(buf[:n])
			if c.s.OnOutput != nil {
				c.s.OnOutput(buf[:n])
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
	select {
	case c.ex = <-c.exit:
		c.gone = true
		res.Status = StatusExited
		if c.ex.signal != "" {
			res.Status = StatusSignaled
		}
	case <-timeout.C:
		c.killTree(c.s.KillGrace)
		res.Status = StatusTimeout
	case <-ctx.Done():
		c.cancelled(ctx, res)
		return
	case <-c.r.stop:
		c.cancelled(ctx, res)
		return
	}
	c.finish()
	c.result(res)
}

// cancelled runs the kill order with a 1 s result deadline (REL-06): the
// tree is confirmed dead or returned still_running while the kill order
// continues in the background (Result.Done).
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

// killTree is the kill order of §A.4: TERM to the tree, up to grace for it
// to exit, then KILL.
func (c *call) killTree(grace time.Duration) {
	c.note(c.p.terminate())
	if !c.waitDead(time.Now().Add(grace)) {
		c.note(c.p.kill())
	}
}

// waitDead reaps the leader and polls the tree until it is gone or the
// deadline passes.
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

// finish kills what is left of the tree (stragglers of an exited leader,
// setsid escapees by marker), reaps the leader and drains the output.
func (c *call) finish() {
	c.note(c.p.kill())
	c.sweep()
	if !c.gone {
		c.ex = <-c.exit
		c.gone = true
	}
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

// sweep kills every process carrying this call's marker (§A.4).
func (c *call) sweep() {
	id := []byte(c.id)
	for range sweepRounds {
		pids, err := markerPids(func(v []byte) bool { return bytes.Equal(v, id) })
		c.note(err)
		if len(pids) == 0 {
			return
		}
		for _, pid := range pids {
			c.note(killPid(pid))
		}
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

// newExecID is "<owner pid>.<random>": the owner pid lets the crash sweep
// tell trees of a dead damnbot from those of a live one.
func newExecID() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand never fails on supported platforms
	return strconv.Itoa(os.Getpid()) + "." + hex.EncodeToString(b[:])
}

// withMarker returns env with MarkerEnv=id and DAMNBOT=1 (hard values,
// §A.6), replacing any inherited ones.
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
	if s.KillGrace == 0 {
		s.KillGrace = DefaultKillGrace
	}
	s.KillGrace = min(max(s.KillGrace, MinKillGrace), MaxKillGrace)
	return nil
}

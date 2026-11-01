// Package exec background-process registry (EXEC-AND-CODING §A.5, ADR-072,
// EXEC-009): bounded live processes per session, ring-buffered output,
// process_list/output/wait/send/kill semantics. Processes die with the
// registry: they never survive a damnbot restart.
package exec

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Background limits (EXEC-AND-CODING §A.5).
const (
	MaxLivePerSession = 8
	MaxLiveGlobal     = 32
	ProcessLogCap     = 8 << 20
	ProcessRetention  = 30 * time.Minute
	MaxFinishedProc   = 64
)

// ProcessStatus is the closed state of a background process.
type ProcessStatus string

// Process statuses.
const (
	ProcessRunning   ProcessStatus = "running"
	ProcessExited    ProcessStatus = "exited"
	ProcessSignaled  ProcessStatus = "signaled"
	ProcessCancelled ProcessStatus = "cancelled"
)

// Process errors.
var (
	ErrProcessNotFound = errors.New("exec: process not found")
	ErrProcessLimit    = errors.New("exec: process limit reached")
	ErrProcessClosed   = errors.New("exec: process registry closed")
	ErrProcessGone     = errors.New("exec: process already finished")
)

// ProcessInfo is one process_list entry.
type ProcessInfo struct {
	ID        string        `json:"id"`
	Session   string        `json:"session"`
	Command   string        `json:"command"`
	Status    ProcessStatus `json:"status"`
	PID       int           `json:"pid"`
	PGID      int           `json:"pgid"`
	Started   time.Time     `json:"started"`
	ExitCode  *int          `json:"exit_code,omitempty"`
	Signal    string        `json:"signal,omitempty"`
	RuntimeMS int64         `json:"runtime_ms"`
}

// PollResult is one process_output answer.
type PollResult struct {
	Info   ProcessInfo `json:"info"`
	Output []byte      `json:"output"`
	Offset int64       `json:"offset"` // next byte offset
	Lines  int         `json:"lines"`
}

// bgProc is one registered process.
type bgProc struct {
	info    ProcessInfo
	cmd     *cmdHandle
	buf     *ring
	done    chan struct{}
	exited  bool
	exitErr error
}

// Background owns the processes of one damnbot process.
type Background struct {
	mu       sync.Mutex
	per      map[string]map[string]*bgProc
	all      map[string]*bgProc
	seq      int
	closed   bool
	now      func() time.Time
	start    func(spawnSpec) (*cmdHandle, error)
	finished int // completed entries pruned by retention/max
}

type spawnSpec struct {
	argv []string
	dir  string
	env  []string
	out  *ring
}

// cmdHandle hides the platform process so Windows can report a capability
// refusal without compiling the POSIX implementation.
type cmdHandle struct {
	pid   int
	pgid  int
	stdin interface {
		Write([]byte) (int, error)
		Close() error
	}
	wait func() (int, string, error)
	kill func(signal string) error
}

// NewBackground returns an empty registry.
func NewBackground() *Background {
	return &Background{
		per: map[string]map[string]*bgProc{}, all: map[string]*bgProc{},
		now: time.Now, start: startBackground,
	}
}

// Start launches argv detached (own process group) for session. The caller
// supplies the complete child environment (allowlist enforced upstream).
func (b *Background) Start(ctx context.Context, session string, argv []string, dir string, env []string) (ProcessInfo, error) {
	if len(argv) == 0 {
		return ProcessInfo{}, errors.Join(ErrSpec, errors.New("empty argv"))
	}
	if session == "" {
		return ProcessInfo{}, errors.Join(ErrSpec, errors.New("empty session"))
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ProcessInfo{}, ErrProcessClosed
	}
	liveSession, liveGlobal := 0, 0
	for _, p := range b.per[session] {
		if !p.isExited() {
			liveSession++
		}
	}
	for _, p := range b.all {
		if !p.isExited() {
			liveGlobal++
		}
	}
	if liveGlobal >= MaxLiveGlobal || liveSession >= MaxLivePerSession {
		b.mu.Unlock()
		return ProcessInfo{}, fmt.Errorf("%w: session=%d global=%d", ErrProcessLimit, liveSession, liveGlobal)
	}
	b.seq++
	id := fmt.Sprintf("p%d", b.seq)
	b.mu.Unlock()
	out := newRing(ProcessLogCap)
	cmd, err := b.start(spawnSpec{argv: argv, dir: dir, env: env, out: out})
	if err != nil {
		return ProcessInfo{}, err
	}
	p := &bgProc{
		info: ProcessInfo{
			ID: id, Session: session, Command: displayCommand(argv), Status: ProcessRunning,
			PID: cmd.pid, PGID: cmd.pgid, Started: b.now(),
		},
		cmd: cmd, buf: out, done: make(chan struct{}),
	}
	b.mu.Lock()
	if b.per[session] == nil {
		b.per[session] = map[string]*bgProc{}
	}
	b.per[session][id] = p
	b.all[id] = p
	b.mu.Unlock()
	go b.reap(p)
	return p.info, nil
}

// reap waits for the process once and records the final state.
func (b *Background) reap(p *bgProc) {
	code, sig, err := p.cmd.wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	p.exited, p.exitErr = true, err
	p.info.Status = ProcessExited
	if sig != "" {
		p.info.Status, p.info.Signal = ProcessSignaled, sig
	}
	if code >= 0 {
		p.info.ExitCode = &code
	}
	p.info.RuntimeMS = b.now().Sub(p.info.Started).Milliseconds()
	close(p.done)
}

// lookup returns a process only for its owning session (isolation).
func (b *Background) lookup(session, id string) (*bgProc, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrProcessClosed
	}
	p := b.per[session][id]
	if p == nil {
		return nil, ErrProcessNotFound
	}
	return p, nil
}

// List returns at most 40 entries of session, newest first.
func (b *Background) List(session string) []ProcessInfo {
	b.pruneFinished()
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ProcessInfo, 0, len(b.per[session]))
	for _, p := range b.per[session] {
		out = append(out, p.snapshot())
	}
	slices.SortFunc(out, func(a, b ProcessInfo) int { return strings.Compare(b.ID, a.ID) })
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// Poll returns buffered output at or after since (0 = from the oldest byte).
func (b *Background) Poll(session, id string, since int64, tailLines int, grep string) (PollResult, error) {
	p, err := b.lookup(session, id)
	if err != nil {
		return PollResult{}, err
	}
	data, next := p.buf.bytesSince(since)
	if tailLines > 0 {
		data = tail(data, tailLines)
	}
	if grep != "" {
		re, err := regexp.Compile(grep)
		if err != nil {
			return PollResult{}, fmt.Errorf("%w: grep: %w", ErrSpec, err)
		}
		data = filterLines(data, re)
	}
	return PollResult{Info: p.snapshot(), Output: data, Offset: next, Lines: lineCount(data)}, nil
}

// Write sends text to the process stdin (pty/pipe with open stdin).
func (b *Background) Write(session, id, text string, enter bool) error {
	p, err := b.lookup(session, id)
	if err != nil {
		return err
	}
	if p.cmd.stdin == nil {
		return fmt.Errorf("%w: stdin closed", ErrProcessGone)
	}
	payload := text
	if enter {
		payload += "\n"
	}
	if _, err := p.cmd.stdin.Write([]byte(payload)); err != nil {
		return fmt.Errorf("exec: process write: %w", err)
	}
	return nil
}

// Kill sends signal (TERM|INT|KILL) and returns the final status when the
// process is gone; TERM escalates to KILL after DefaultKillGrace.
func (b *Background) Kill(ctx context.Context, session, id, signal string) (ProcessInfo, error) {
	p, err := b.lookup(session, id)
	if err != nil {
		return ProcessInfo{}, err
	}
	switch signal {
	case "", "TERM", "INT", "KILL":
	default:
		return ProcessInfo{}, fmt.Errorf("%w: signal %q", ErrSpec, signal)
	}
	if signal == "" {
		signal = "TERM"
	}
	if p.isExited() {
		return p.snapshot(), nil
	}
	if err := p.cmd.kill(signal); err != nil {
		return ProcessInfo{}, err
	}
	select {
	case <-p.done:
		return p.snapshot(), nil
	case <-time.After(DefaultKillGrace):
		if signal != "KILL" {
			//nolint:errcheck,gosec // escalation is best effort; status is returned anyway
			p.cmd.kill("KILL")
		}
		select {
		case <-p.done:
		case <-ctx.Done():
		}
		return p.snapshot(), nil
	}
}

// Wait returns the final status or stillRunning after timeout.
func (b *Background) Wait(ctx context.Context, session, id string, timeout time.Duration) (ProcessInfo, bool, error) {
	p, err := b.lookup(session, id)
	if err != nil {
		return ProcessInfo{}, false, err
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-p.done:
		return p.snapshot(), false, nil
	case <-t.C:
		return p.snapshot(), true, nil
	case <-ctx.Done():
		return p.snapshot(), true, fmt.Errorf("exec: process wait: %w", ctx.Err())
	}
}

// Close kills every live process and empties the registry (shutdown).
func (b *Background) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	procs := make([]*bgProc, 0, len(b.all))
	for _, p := range b.all {
		procs = append(procs, p)
	}
	b.mu.Unlock()
	var errs error
	for _, p := range procs {
		if !p.isExited() {
			errs = errors.Join(errs, p.cmd.kill("KILL"))
		}
	}
	for _, p := range procs {
		select {
		case <-p.done:
		case <-ctx.Done():
			return errors.Join(errs, ctx.Err())
		}
	}
	return errs
}

// snapshot copies the state under the registry lock.
func (p *bgProc) snapshot() ProcessInfo {
	return p.info
}

func (p *bgProc) isExited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// displayCommand is the ≤80-char preview of process_list.
func displayCommand(argv []string) string {
	s := strings.Join(argv, " ")
	if len(s) <= 80 {
		return s
	}
	return s[:77] + "..."
}

// ring is the bounded output buffer (oldest bytes dropped).
type ring struct {
	mu    sync.Mutex
	data  []byte
	total int64
	cap   int
}

func newRing(cap int) *ring { return &ring{cap: cap} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total += int64(len(p))
	r.data = append(r.data, p...)
	if len(r.data) > r.cap {
		r.data = append([]byte(nil), r.data[len(r.data)-r.cap:]...)
	}
	return len(p), nil
}

// bytesSince returns bytes at absolute offset since (clamped to retained
// data) and the next absolute offset.
func (r *ring) bytesSince(since int64) ([]byte, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := r.total - int64(len(r.data))
	if since < start || since > r.total {
		since = start
	}
	out := append([]byte(nil), r.data[since-start:]...)
	return out, r.total
}

func tail(data []byte, lines int) []byte {
	if lines <= 0 {
		return data
	}
	n := 0
	for i := range slices.Backward(data) {
		if data[i] == '\n' && i != len(data)-1 {
			n++
			if n >= lines {
				return data[i+1:]
			}
		}
	}
	return data
}

func filterLines(data []byte, re *regexp.Regexp) []byte {
	var out []byte
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if line != "" && re.MatchString(line) {
			out = append(out, line...)
		}
	}
	return out
}

func lineCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := strings.Count(string(data), "\n")
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// pruneFinished drops completed entries older than retention or beyond
// MaxFinishedProc; live entries are never touched.
func (b *Background) pruneFinished() {
	b.mu.Lock()
	defer b.mu.Unlock()
	type ent struct {
		p   *bgProc
		end time.Time
	}
	var done []ent
	for _, p := range b.all {
		if p.isExited() {
			done = append(done, ent{p, p.info.Started.Add(time.Duration(p.info.RuntimeMS) * time.Millisecond)})
		}
	}
	slices.SortFunc(done, func(a, b ent) int { return a.end.Compare(b.end) })
	for i, e := range done {
		if b.now().Sub(e.end) > ProcessRetention || len(done)-i > MaxFinishedProc {
			delete(b.all, e.p.info.ID)
			delete(b.per[e.p.info.Session], e.p.info.ID)
			b.finished++
		}
	}
}

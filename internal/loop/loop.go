package loop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// ErrTurnInProgress is returned by Prompt when the session already has an
// active turn (SPEC §4.2).
var ErrTurnInProgress = errors.New("loop: turn in progress")

// ErrNoProvider and ErrNoAssembler reject a Loop that cannot run a turn.
var (
	ErrNoProvider  = errors.New("loop: provider required")
	ErrNoAssembler = errors.New("loop: assembler required")
)

// Loop runs at most one turn per session (SPEC §4.2). Safe for concurrent use.
type Loop struct {
	cfg  Config
	deps Deps

	mu     sync.Mutex
	active map[string]*activeTurn
}

// activeTurn is one running turn's cancellation handle. user records that the
// cancellation came from Cancel rather than from the caller's own context.
type activeTurn struct {
	cancel context.CancelFunc
	user   atomic.Bool
}

// New validates the dependencies and applies defaults.
func New(cfg *Config, deps *Deps) (*Loop, error) {
	if deps == nil || deps.Provider == nil {
		return nil, ErrNoProvider
	}
	if deps.Assembler == nil {
		return nil, ErrNoAssembler
	}
	c := Config{}
	if cfg != nil {
		c = *cfg
	}
	withDefaults(&c)
	return &Loop{cfg: c, deps: *deps, active: map[string]*activeTurn{}}, nil
}

// withDefaults fills every zero bound with its SPEC default.
func withDefaults(c *Config) {
	if c.MaxIterations <= 0 {
		c.MaxIterations = 40
	}
	if c.MaxIdentical <= 0 {
		c.MaxIdentical = 3
	}
	if c.MaxInline <= 0 {
		c.MaxInline = 4000 // P5: inline ≤ 4.000 char
	}
	if c.MaxInlineTurn <= 0 {
		c.MaxInlineTurn = 24000 // P5: aggregate ≤ 24.000 char
	}
	if c.StreamBuffer <= 0 {
		c.StreamBuffer = 16
	}
	if c.ParallelTools <= 0 {
		c.ParallelTools = 4 // SPEC §4.2 parallel_tools default
	}
	if c.KillGrace <= 0 {
		c.KillGrace = time.Second // REL-06
	}
	if c.CompactAt <= 0 {
		c.CompactAt = 75
	}
	if c.OverflowAt <= 0 {
		c.OverflowAt = 90
	}
	if c.Now == nil {
		c.Now = time.Now // the one wall-clock seam; tests inject a fixed clock
	}
	if c.NewTurnID == nil {
		c.NewTurnID = newTurnID
	}
}

// newTurnID returns a 128-bit random turn id (identity only needs to be
// unique and opaque).
func newTurnID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "turn-0000000000000000" // never a wall-clock read
	}
	return hex.EncodeToString(b[:])
}

// Prompt starts one turn and returns its stream. The caller MUST drain the
// channel until it is closed, even after canceling ctx: the turn delivers its
// final stop update through it, and E-29 relies on that drain. A second turn
// on the same session returns ErrTurnInProgress.
func (l *Loop) Prompt(ctx context.Context, sessionID string, in Input) (<-chan event.Update, error) {
	if sessionID == "" {
		return nil, errors.New("loop: empty session id")
	}
	turnID := l.cfg.NewTurnID()
	turnCtx, cancel := context.WithCancel(ctx)

	l.mu.Lock()
	if _, ok := l.active[sessionID]; ok {
		l.mu.Unlock()
		cancel()
		return nil, ErrTurnInProgress
	}
	l.active[sessionID] = &activeTurn{cancel: cancel}
	l.mu.Unlock()

	out := make(chan event.Update, l.cfg.StreamBuffer) //damnbot:buffered stream, caller drains until close
	t := &turn{
		cfg: l.cfg, deps: l.deps, sessionID: sessionID, turnID: turnID,
		input: in, out: out, start: l.cfg.Now(), allowed: map[string]bool{},
		seen: map[[32]byte]int{},
	}
	// E-26: the turn goroutine starts only through boundary, which also
	// recovers a panic into a *boundary.PanicError (the stream is closed).
	_ = boundary.Go(turnCtx, "loop.turn", func(ctx context.Context) error {
		defer close(out)
		defer cancel()
		defer l.finish(sessionID)
		t.run(ctx)
		return nil
	})
	return out, nil
}

// finish removes the session's active turn. It runs before the stream closes.
func (l *Loop) finish(sessionID string) {
	l.mu.Lock()
	delete(l.active, sessionID)
	l.mu.Unlock()
}

// Cancel cancels the session's active turn, if any. The turn reports
// cancelled (or cancelled_confirmed when every in-flight tool confirmed it
// stopped inside the kill grace).
func (l *Loop) Cancel(sessionID string) {
	l.mu.Lock()
	h := l.active[sessionID]
	l.mu.Unlock()
	if h != nil {
		h.user.Store(true)
		h.cancel()
	}
}

// Active reports whether sessionID has a running turn.
func (l *Loop) Active(sessionID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.active[sessionID]
	return ok
}

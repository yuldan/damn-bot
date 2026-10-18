// Package boundary is the only place that starts goroutines (E-26) and calls
// recover (E-24). A panic in the guarded function becomes a *PanicError
// (errors.Is ErrPanic) returned to the owner and is logged at ERROR, so it
// never crashes the process and is never swallowed (P14: a module panic
// marks the module failed, a tool call panic becomes code internal).
//
// Every goroutine is owned: Go returns a *Task to wait on, Group.Wait waits
// for all members. Functions receive the caller's context and must return
// when it is canceled (E-29); boundary never abandons a goroutine.
package boundary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"golang.org/x/sync/errgroup"
)

// ErrPanic matches every *PanicError.
var ErrPanic = errors.New("boundary: panic")

// PanicError is a panic recovered in a guarded function.
type PanicError struct {
	Name  string // goroutine / call-site name given to Go, Run or Group.Go
	Value any    // value passed to panic
	Stack []byte // stack of the panicking goroutine (debug.Stack)
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("boundary: panic in %s: %v", e.Name, e.Value)
}

// Is makes errors.Is(err, ErrPanic) true.
func (e *PanicError) Is(target error) bool { return target == ErrPanic }

// Unwrap exposes a panic value that is an error (e.g. runtime.Error).
func (e *PanicError) Unwrap() error {
	if err, ok := e.Value.(error); ok {
		return err
	}
	return nil
}

// Run calls fn(ctx) on the current goroutine and returns its error, or a
// *PanicError if fn panics. Use it where a panic must not cross a dispatch
// boundary (tool call, module hook) without starting a goroutine.
func Run(ctx context.Context, name string, fn func(context.Context) error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = recovered(ctx, name, v)
		}
	}()
	return fn(ctx)
}

// recovered builds the *PanicError and logs it. The panic value may carry
// message content, so the log has only its type and the stack (P29); the
// value itself goes to the owner in the error.
func recovered(ctx context.Context, name string, v any) error {
	pe := &PanicError{Name: name, Value: v, Stack: debug.Stack()}
	slog.ErrorContext(ctx, "panic recovered",
		slog.String("goroutine", name),
		slog.String("panic_type", fmt.Sprintf("%T", v)),
		slog.String("stack", string(pe.Stack)))
	return pe
}

// Task is a goroutine started by Go.
type Task struct {
	done chan struct{} //damnbot:unbuffered closed once when the goroutine returns, never sent on
	err  error
}

// Go runs fn(ctx) in a new goroutine guarded like Run. The caller owns the
// returned Task and must Wait for it (or select on Done) before it drops
// the last reference; fn must return once ctx is canceled (E-29).
func Go(ctx context.Context, name string, fn func(context.Context) error) *Task {
	t := &Task{done: make(chan struct{})} //damnbot:unbuffered close-only completion signal
	go func() {
		defer close(t.done)
		t.err = Run(ctx, name, fn)
	}()
	return t
}

// Done is closed when the goroutine has returned.
func (t *Task) Done() <-chan struct{} { return t.done }

// Wait blocks until the goroutine returns and reports fn's error or the
// recovered *PanicError.
func (t *Task) Wait() error {
	<-t.done
	return t.err
}

// Group is errgroup.Group whose members are guarded like Run: a panic in a
// member is its error, so it cancels the group context and is returned by
// Wait. Callers bound every group with SetLimit (E-27).
type Group struct {
	g *errgroup.Group
}

// WithContext returns a Group and a context canceled when a member fails,
// panics, or Wait returns.
func WithContext(ctx context.Context) (*Group, context.Context) {
	g, gctx := errgroup.WithContext(ctx)
	return &Group{g: g}, gctx
}

// SetLimit bounds the number of active members to n (see errgroup).
func (g *Group) SetLimit(n int) { g.g.SetLimit(n) }

// Go starts fn(ctx) as a member named name, blocking while the limit is
// reached. ctx is normally the context returned by WithContext.
func (g *Group) Go(ctx context.Context, name string, fn func(context.Context) error) {
	g.g.Go(func() error { return Run(ctx, name, fn) })
}

// Wait blocks until every member has returned and reports the first error.
func (g *Group) Wait() error {
	if err := g.g.Wait(); err != nil {
		return fmt.Errorf("boundary: group: %w", err)
	}
	return nil
}

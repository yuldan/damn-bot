package boundary

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
)

// Scope is one run's resource boundary (E-29, GAP-REL-12): closers released
// together by Close. It is safe for concurrent Add/Close.
type Scope struct {
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	closers []io.Closer
}

// NewScope returns a scope and the child context its Close cancels.
func NewScope(parent context.Context) (*Scope, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	return &Scope{cancel: cancel}, ctx
}

// Add registers a closer. A scope that is already closed closes it at once.
func (s *Scope) Add(c io.Closer) error {
	if c == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("scope add: %w", c.Close())
	}
	s.closers = append(s.closers, c)
	s.mu.Unlock()
	return nil
}

// Close cancels the context and closes every registered resource in reverse
// order. It is idempotent and never panics on a nil Scope.
func (s *Scope) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	closers := s.closers
	s.closers = nil
	s.mu.Unlock()
	s.cancel()
	var errs []error
	for _, c := range slices.Backward(closers) {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

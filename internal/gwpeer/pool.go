package gwpeer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
)

// ErrPoolFallback reports EMFILE exhaustion with no writer fallback.
var ErrPoolFallback = errors.New("gwpeer: read pool exhausted without a writer fallback")

// ReadPool bounds the database/file handles a multiplexed gateway opens at
// once (EC-STATE-004). When the global cap is reached, or opening returns
// EMFILE, the caller falls back to the writer path instead of failing the
// request.
type ReadPool struct {
	// Max is the global connection cap; zero uses DefaultReadPoolMax.
	Max int
	// Open acquires one read handle.
	Open func(ctx context.Context) (io.Closer, error)
	// Writer is the fallback when a reader cannot be opened.
	Writer func(ctx context.Context) error

	mu    sync.Mutex
	inUse int
}

// DefaultReadPoolMax is the EC-STATE-004 global cap.
const DefaultReadPoolMax = 4

// With runs read with one bounded handle. fallback is true when the request
// took the writer path (cap reached or EMFILE); err is then the writer's error.
func (p *ReadPool) With(ctx context.Context, read func(ctx context.Context, conn io.Closer) error) (bool, error) {
	conn, fallback, err := p.acquire(ctx)
	if fallback {
		return true, p.writer(ctx, err)
	}
	if err != nil {
		return false, err
	}
	rerr := read(ctx, conn)
	cerr := conn.Close()
	p.release()
	if jerr := errors.Join(rerr, cerr); jerr != nil {
		return false, fmt.Errorf("gwpeer: pooled read: %w", jerr)
	}
	return false, nil
}

// acquire reserves one slot and opens its handle. A full pool or EMFILE is a
// fallback, not an error; every other open failure is returned.
func (p *ReadPool) acquire(ctx context.Context) (io.Closer, bool, error) {
	if p.Open == nil {
		return nil, true, ErrPoolFallback
	}
	p.mu.Lock()
	if p.inUse >= p.max() {
		p.mu.Unlock()
		return nil, true, nil
	}
	p.inUse++
	p.mu.Unlock()
	conn, err := p.Open(ctx)
	if err == nil {
		return conn, false, nil
	}
	p.release()
	if errors.Is(err, syscall.EMFILE) {
		return nil, true, nil
	}
	return nil, false, fmt.Errorf("gwpeer: open read handle: %w", err)
}

func (p *ReadPool) release() {
	p.mu.Lock()
	if p.inUse > 0 {
		p.inUse--
	}
	p.mu.Unlock()
}

func (p *ReadPool) writer(ctx context.Context, cause error) error {
	if p.Writer == nil {
		if cause != nil {
			return fmt.Errorf("%w: %w", ErrPoolFallback, cause)
		}
		return ErrPoolFallback
	}
	if err := p.Writer(ctx); err != nil {
		return fmt.Errorf("gwpeer: writer fallback: %w", err)
	}
	return nil
}

func (p *ReadPool) max() int {
	if p.Max > 0 {
		return p.Max
	}
	return DefaultReadPoolMax
}

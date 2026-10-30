package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrChatQueueClosed rejects work submitted after Close.
var ErrChatQueueClosed = errors.New("gateway: chat queue is closed")

// ErrInvalidChatWork rejects a job without a chat key or a function.
var ErrInvalidChatWork = errors.New("gateway: chat queue work is invalid")

// ChatQueue dispatches work per chat: jobs of one chat run in submission order
// on a single lane, while different chats run concurrently up to MaxParallel
// lanes (EC-PLAT-015). Submit never blocks the caller; the poller keeps
// receiving updates while a turn runs.
//
// A lane lives as long as it has work: the lane goroutine exits as soon as its
// FIFO is empty, so idle chats cost nothing. The global semaphore bounds
// concurrent jobs to MaxParallel, matching gateway.max_parallel_turns.
type ChatQueue struct {
	sem chan struct{}
	mu  sync.Mutex
	wg  sync.WaitGroup
	// lanes maps a chat key to its FIFO. A lane is present only while it has
	// queued work or a running drain; running guards against two drains.
	lanes  map[string]*lane
	closed bool
}

type lane struct {
	jobs    []func(context.Context)
	running bool
}

// NewChatQueue returns a queue with at most parallel concurrent jobs. A
// non-positive bound uses DefaultMaxParallelTurns.
func NewChatQueue(parallel int) *ChatQueue {
	if parallel <= 0 {
		parallel = DefaultMaxParallelTurns
	}
	return &ChatQueue{sem: make(chan struct{}, parallel), lanes: make(map[string]*lane)}
}

// Submit appends work to key's FIFO. The first job of an idle chat starts its
// lane; ctx bounds the lane lifetime (a cancelled context drops queued work,
// which is what shutdown wants).
func (q *ChatQueue) Submit(ctx context.Context, key string, work func(context.Context)) error {
	if key == "" || work == nil {
		return fmt.Errorf("%w: chat and work are required", ErrInvalidChatWork)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrChatQueueClosed
	}
	l := q.lanes[key]
	if l == nil {
		l = &lane{}
		q.lanes[key] = l
	}
	l.jobs = append(l.jobs, work)
	if !l.running {
		l.running = true
		q.wg.Add(1)
		go q.drain(ctx, key)
	}
	return nil
}

// Stats reports the jobs waiting in chat lanes and the jobs holding a
// concurrency slot. It is read-only and never blocks a lane.
func (q *ChatQueue) Stats() (queued, active int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, l := range q.lanes {
		queued += len(l.jobs)
	}
	return queued, len(q.sem)
}

// Close refuses new work and waits for running jobs. Callers cancel the
// context passed to Submit first: a lane blocked on the global semaphore
// returns on cancellation instead of holding Close forever.
func (q *ChatQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.wg.Wait()
}

// drain runs one chat's FIFO to completion. It takes a concurrency slot before
// popping, so a job waiting for a slot stays visible as queued in Stats.
func (q *ChatQueue) drain(ctx context.Context, key string) {
	defer q.wg.Done()
	for {
		if !q.acquire(ctx) {
			q.drop(key)
			return
		}
		work, ok := q.next(key)
		if !ok {
			<-q.sem
			return
		}
		work(ctx)
		<-q.sem
	}
}

// next pops the head of key's FIFO; ok=false also deletes an empty lane.
func (q *ChatQueue) next(key string) (func(context.Context), bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	l := q.lanes[key]
	if l == nil || len(l.jobs) == 0 {
		delete(q.lanes, key)
		return nil, false
	}
	work := l.jobs[0]
	l.jobs = l.jobs[1:]
	return work, true
}

// acquire takes one global slot, honouring cancellation.
func (q *ChatQueue) acquire(ctx context.Context) bool {
	select {
	case q.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// drop discards key's queue after the lane context ended.
func (q *ChatQueue) drop(key string) {
	q.mu.Lock()
	delete(q.lanes, key)
	q.mu.Unlock()
}

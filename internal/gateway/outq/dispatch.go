package outq

import (
	"context"
	"time"
)

// result is one completed send reported by a worker.
type result struct {
	job *job
	err error
}

// loop is the single dispatcher: it picks the fairest ready part, hands it to
// a worker, and records the outcome. All queue state stays on this goroutine
// so ordering and fairness need one lock.
func (s *Scheduler) loop(ctx context.Context, work chan<- *job, done <-chan result) {
	idle := 10 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		j, wait, ok := s.pickLocked(s.now())
		s.mu.Unlock()
		if ok {
		send:
			for {
				select {
				case work <- j:
					break send
				case r := <-done:
					s.finish(r)
				case <-ctx.Done():
					return
				}
			}
			continue
		}
		if wait <= 0 {
			wait = idle
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case r := <-done:
			timer.Stop()
			s.finish(r)
		case <-timer.C:
		}
	}
}

// pickLocked returns the next part to send, or why the earliest one is not
// ready. It consumes one token per part from the pacer.
func (s *Scheduler) pickLocked(at time.Time) (*job, time.Duration, bool) {
	var wait time.Duration
	for pri := int(PriorityHigh); pri >= int(PriorityLow); pri-- {
		if j, ok := s.pickPriority(at, Priority(pri), &wait); ok {
			return j, 0, true
		}
	}
	return nil, wait, false
}

// pickPriority scans the round-robin order for one ready chat of pri.
func (s *Scheduler) pickPriority(at time.Time, pri Priority, wait *time.Duration) (*job, bool) {
	for offset := range s.order {
		i := (s.rr + offset) % len(s.order)
		chat := s.order[i]
		head := s.queues[chat]
		if len(head) == 0 || head[0].priority != pri {
			continue
		}
		if j, ok := s.takeChat(i, chat, at, wait); ok {
			return j, true
		}
	}
	return nil, false
}

// takeChat tries to consume one token for chat's head.
func (s *Scheduler) takeChat(i int, chat string, at time.Time, wait *time.Duration) (*job, bool) {
	j := s.queues[chat][0]
	if d := j.nextAt.Sub(at); d > 0 {
		*wait = minPositive(*wait, d)
		return nil, false
	}
	ok, d := s.pacer.TakeChat(j.msg.Platform, chat, j.msg.Group, at)
	if !ok {
		s.stats.Throttled++
		s.stats.MaxWait = max(s.stats.MaxWait, d)
		*wait = minPositive(*wait, d)
		return nil, false
	}
	s.queues[chat] = s.queues[chat][1:]
	if len(s.queues[chat]) == 0 {
		s.removeLocked(i)
		if len(s.order) > 0 {
			s.rr = i % len(s.order)
		}
	} else {
		s.rr = (i + 1) % len(s.order)
	}
	if len(s.order) == 0 {
		s.rr = 0
	}
	s.inflight++
	return j, true
}

func (s *Scheduler) removeLocked(i int) {
	chat := s.order[i]
	delete(s.queues, chat)
	s.order = append(s.order[:i], s.order[i+1:]...)
	if len(s.order) == 0 {
		s.rr = 0
		return
	}
	s.rr %= len(s.order)
}

// worker executes exactly one part. It never sleeps: pacing belongs to the
// dispatcher and the adapter Send callback is contractually ctx-aware.
func (s *Scheduler) worker(ctx context.Context, work <-chan *job, done chan<- result) {
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-work:
			if !ok {
				return
			}
			err := Permit{owner: s}.Do(ctx, func(c context.Context) error {
				return s.call(c, j)
			})
			select {
			case done <- result{job: j, err: err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (s *Scheduler) call(ctx context.Context, j *job) error {
	if j.overflow != "" {
		return j.msg.Overflow(ctx, j.overflow)
	}
	return j.msg.Send(ctx, j.part)
}

// finish records one send outcome and requeues a retryable failure at the head
// of its chat FIFO, preserving per-chat order.
func (s *Scheduler) finish(r result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight--
	if r.err == nil {
		s.stats.Sent++
		s.signalLocked()
		return
	}
	j := r.job
	now := s.now()
	delay := s.backoff(j.attempts + 1)
	if j.msg.RetryAfter != nil {
		if ra, ok := j.msg.RetryAfter(r.err); ok && ra > 0 {
			delay = max(delay, ra)
			s.pacer.Penalize(j.msg.Chat, now.Add(ra))
		}
	}
	j.attempts++
	if j.attempts >= s.maxAttempts || s.closed {
		s.stats.Failed++
		if s.onError != nil {
			s.onError(j.msg, r.err)
		}
		s.signalLocked()
		return
	}
	j.nextAt = now.Add(delay)
	s.stats.Retried++
	if _, ok := s.queues[j.msg.Chat]; !ok {
		s.order = append(s.order, j.msg.Chat)
	}
	s.queues[j.msg.Chat] = append([]*job{j}, s.queues[j.msg.Chat]...)
	s.signalLocked()
}

func minPositive(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return min(a, b)
}

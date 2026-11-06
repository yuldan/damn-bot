package a2a

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// State is the A2A task state machine.
type State string

// Task states of the inbound server. submitted → working → completed|failed;
// a cancel from any non-terminal state ends in canceled.
const (
	StateSubmitted State = "submitted"
	StateWorking   State = "working"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
)

// Terminal reports whether no transition may follow.
func (s State) Terminal() bool {
	switch s {
	case StateSubmitted, StateWorking:
		return false
	case StateCompleted, StateFailed, StateCanceled:
		return true
	default:
		return false
	}
}

// Task is one immutable snapshot handed to callers.
type Task struct {
	ID        string
	State     State
	Text      string
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func taskFromWire(w *taskWire) (Task, error) {
	created, err := time.Parse(time.RFC3339Nano, w.CreatedAt)
	if err != nil {
		return Task{}, fmt.Errorf("%w: createdAt", ErrBadResponse)
	}
	updated, err := time.Parse(time.RFC3339Nano, w.UpdatedAt)
	if err != nil {
		return Task{}, fmt.Errorf("%w: updatedAt", ErrBadResponse)
	}
	switch w.State {
	case StateSubmitted, StateWorking, StateCompleted, StateFailed, StateCanceled:
	default:
		return Task{}, fmt.Errorf("%w: state %q", ErrBadResponse, w.State)
	}
	return Task{ID: w.ID, State: w.State, Text: w.Text, Error: w.Error, CreatedAt: created, UpdatedAt: updated}, nil
}

func (t *Task) wire() taskWire {
	return taskWire{
		ID: t.ID, State: t.State, Text: t.Text, Error: t.Error,
		CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// maxTasks bounds the in-memory task table; terminal tasks are evicted first.
const maxTasks = 256

// taskStore is the idempotent task table. A task id is the idempotency key:
// create reports false for a duplicate and the caller must not execute it.
type taskStore struct {
	mu      sync.Mutex
	tasks   map[string]*Task
	order   []string
	created int
}

func newTaskStore() *taskStore {
	return &taskStore{tasks: make(map[string]*Task, maxTasks)}
}

func (s *taskStore) create(id string, now time.Time) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.tasks[id]; ok {
		return *existing, false
	}
	t := &Task{ID: id, State: StateSubmitted, CreatedAt: now, UpdatedAt: now}
	s.tasks[id] = t
	s.order = append(s.order, id)
	s.created++
	s.evict()
	return *t, true
}

func (s *taskStore) get(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, false
	}
	return *t, true
}

// update applies fn unless the task already reached a terminal state.
func (s *taskStore) update(id string, now time.Time, fn func(*Task)) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, false
	}
	if t.State.Terminal() {
		return *t, false
	}
	fn(t)
	t.UpdatedAt = now
	return *t, true
}

func (s *taskStore) evict() {
	if len(s.order) <= maxTasks {
		return
	}
	drop := len(s.order) - maxTasks
	kept := s.order[:0]
	for _, id := range s.order {
		t, ok := s.tasks[id]
		if drop > 0 && ok && t.State.Terminal() {
			delete(s.tasks, id)
			drop--
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept
	for len(s.tasks) > maxTasks {
		id := s.order[0]
		s.order = s.order[1:]
		delete(s.tasks, id)
	}
}

// wrapUntrusted marks model-facing peer text (SPEC §14.5). A closing tag in
// the payload is escaped so the wrapper cannot be terminated early.
func wrapUntrusted(text string) string {
	text = strings.ReplaceAll(text, "</untrusted>", "&lt;/untrusted&gt;")
	return "<untrusted source=\"a2a\">" + text + "</untrusted>"
}

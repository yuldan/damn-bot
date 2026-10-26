package acp

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// State is a session lifecycle state.
type State uint8

// Session states. A session starts New, becomes Active on the first prompt,
// and is Closed exactly once.
const (
	StateNew State = iota
	StateActive
	StateClosed
)

func (s State) String() string {
	switch s {
	case StateNew:
		return "new"
	case StateActive:
		return "active"
	case StateClosed:
		return "closed"
	}
	return "unknown"
}

// Session errors.
var (
	ErrSessionExists   = errors.New("acp: session already exists")
	ErrSessionNotFound = errors.New("acp: session not found")
	ErrSessionClosed   = errors.New("acp: session is closed")
	ErrOutboxFull      = errors.New("acp: session outbox is full")
	ErrInvalidSession  = errors.New("acp: invalid session id")
)

// DefaultOutbox is the per-session outbox capacity. A full outbox is a
// backpressure signal, never a blocking write: the connection's single writer
// drains it.
const DefaultOutbox = 64

// Session is one ACP session's protocol state: exactly one outbox, never a
// shared dispatcher, transcript, or taint (ADR-199).
type Session struct {
	ID string

	mu     sync.Mutex
	state  State
	outbox chan []byte
	done   chan struct{}
}

// NewSession validates id and returns a New session with cap outbox slots.
func NewSession(id string, cap int) (*Session, error) {
	if err := ValidateSessionID(id); err != nil {
		return nil, err
	}
	if cap <= 0 {
		cap = DefaultOutbox
	}
	return &Session{
		ID: id, state: StateNew,
		outbox: make(chan []byte, cap),
		done:   make(chan struct{}),
	}, nil
}

// ValidateSessionID keeps ids to a bounded printable ASCII token.
func ValidateSessionID(id string) error {
	if id == "" || len(id) > 128 || !utf8.ValidString(id) {
		return ErrInvalidSession
	}
	for _, r := range id {
		if r > unicode.MaxASCII || r < 0x20 || r == 0x7f || strings.ContainsRune(" \t/\\", r) {
			return ErrInvalidSession
		}
	}
	return nil
}

// State returns the current lifecycle state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Activate moves New → Active. Activating an active session is idempotent;
// activating a closed one fails.
func (s *Session) Activate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case StateNew:
		s.state = StateActive
		return nil
	case StateActive:
		return nil
	case StateClosed:
		return ErrSessionClosed
	}
	return ErrSessionClosed
}

// Close moves any non-closed state to Closed; Send refuses afterwards.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateClosed {
		return nil
	}
	s.state = StateClosed
	close(s.done)
	return nil
}

// Done is closed when the session closes.
func (s *Session) Done() <-chan struct{} { return s.done }

// Outbox is the session's single outbound queue, drained by one writer.
func (s *Session) Outbox() <-chan []byte { return s.outbox }

// Send enqueues a copy of frame. It never blocks: a full outbox returns
// ErrOutboxFull so the caller can slow the producer or close the session.
func (s *Session) Send(frame []byte) error {
	s.mu.Lock()
	closed := s.state == StateClosed
	s.mu.Unlock()
	if closed {
		return ErrSessionClosed
	}
	select {
	case <-s.done:
		return ErrSessionClosed
	default:
	}
	cp := append([]byte(nil), frame...)
	select {
	case s.outbox <- cp:
		return nil
	case <-s.done:
		return ErrSessionClosed
	default:
		return ErrOutboxFull
	}
}

// Registry owns the session table; it never shares per-session state.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*Session
	capacity int
}

// NewRegistry returns an empty registry; capacity is each session's outbox
// size (0 = DefaultOutbox).
func NewRegistry(capacity int) *Registry {
	if capacity <= 0 {
		capacity = DefaultOutbox
	}
	return &Registry{sessions: make(map[string]*Session), capacity: capacity}
}

// Create adds a New session. Duplicate ids are refused.
func (r *Registry) Create(id string) (*Session, error) {
	s, err := NewSession(id, r.capacity)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[id]; ok {
		return nil, ErrSessionExists
	}
	r.sessions[id] = s
	return s, nil
}

// Get returns a session by id.
func (r *Registry) Get(id string) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	return s, ok
}

// Close closes and forgets a session.
func (r *Registry) Close(id string) error {
	r.mu.Lock()
	s, ok := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()
	if !ok {
		return ErrSessionNotFound
	}
	return s.Close()
}

// Len returns the number of live sessions.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// IDs returns the live session ids in sorted order (deterministic logs).
func (r *Registry) IDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// CloseAll closes every session; it is used on connection shutdown.
func (r *Registry) CloseAll() error {
	r.mu.Lock()
	list := make([]*Session, 0, len(r.sessions))
	for id, s := range r.sessions {
		list = append(list, s)
		delete(r.sessions, id)
	}
	r.mu.Unlock()
	var errs []error
	for _, s := range list {
		errs = append(errs, s.Close())
	}
	return errors.Join(errs...)
}

package acp

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestSessionLifecycle(t *testing.T) {
	t.Parallel()
	s, err := NewSession("s1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != StateNew {
		t.Fatalf("state = %s", s.State())
	}
	if err := s.Activate(); err != nil || s.State() != StateActive {
		t.Fatalf("activate = %v, %s", err, s.State())
	}
	if err := s.Activate(); err != nil { // idempotent
		t.Fatalf("re-activate = %v", err)
	}
	if err := s.Close(); err != nil || s.State() != StateClosed {
		t.Fatalf("close = %v, %s", err, s.State())
	}
	if err := s.Close(); err != nil { // idempotent
		t.Fatalf("re-close = %v", err)
	}
	if err := s.Activate(); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("activate closed = %v", err)
	}
	if err := s.Send([]byte("x")); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("send closed = %v", err)
	}
}

func TestSessionIDValidation(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", " ", "a b", "a\tb", "a/b", "a\\b", "héllo", "a\x00b", longID(129)} {
		if _, err := NewSession(bad, 1); !errors.Is(err, ErrInvalidSession) {
			t.Errorf("id %q accepted", bad)
		}
	}
	for _, ok := range []string{"s", "s-1", "S_1", "a.b", "0"} {
		if _, err := NewSession(ok, 1); err != nil {
			t.Errorf("id %q rejected: %v", ok, err)
		}
	}
}

func longID(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

//nolint:gocyclo // one assertion per isolation property
func TestRegistryIsolation(t *testing.T) {
	t.Parallel()
	r := NewRegistry(2)
	a, err := r.Create("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Create("b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create("a"); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("duplicate = %v", err)
	}
	if err := a.Send([]byte("for-a")); err != nil {
		t.Fatal(err)
	}
	if err := b.Send([]byte("for-b")); err != nil {
		t.Fatal(err)
	}
	if got := <-a.Outbox(); string(got) != "for-a" {
		t.Fatalf("a outbox = %q", got)
	}
	if got := <-b.Outbox(); string(got) != "for-b" {
		t.Fatalf("b outbox = %q", got)
	}
	select {
	case got := <-a.Outbox():
		t.Fatalf("a saw another session frame: %q", got)
	default:
	}
	if err := r.Close("a"); err != nil {
		t.Fatal(err)
	}
	if a.State() != StateClosed {
		t.Fatal("closed session still open")
	}
	if b.State() != StateActive && b.State() != StateNew {
		t.Fatalf("closing a changed b: %s", b.State())
	}
	if err := b.Send([]byte("still")); err != nil {
		t.Fatalf("b send after a close: %v", err)
	}
	if got := r.IDs(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("ids = %v", got)
	}
	if err := r.Close("missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("close missing = %v", err)
	}
}

func TestSessionOutboxFull(t *testing.T) {
	t.Parallel()
	s, err := NewSession("s", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := s.Send([]byte("two")); !errors.Is(err, ErrOutboxFull) {
		t.Fatalf("second send = %v, want ErrOutboxFull", err)
	}
}

func TestRegistryConcurrentSessions(t *testing.T) {
	t.Parallel()
	r := NewRegistry(64)
	const sessions = 8
	const per = 32
	for i := range sessions {
		if _, err := r.Create(fmt.Sprintf("s%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, _ := r.Get(fmt.Sprintf("s%d", i))
			for j := range per {
				if err := s.Send([]byte(fmt.Sprintf("%d-%d", i, j))); err != nil {
					t.Errorf("send: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	for i := range sessions {
		s, _ := r.Get(fmt.Sprintf("s%d", i))
		for j := range per {
			want := fmt.Sprintf("%d-%d", i, j)
			if got := string(<-s.Outbox()); got != want {
				t.Fatalf("session %d frame %d = %q, want %q", i, j, got, want)
			}
		}
	}
	if r.Len() != sessions {
		t.Fatalf("len = %d", r.Len())
	}
}

func TestRegistryCloseAll(t *testing.T) {
	t.Parallel()
	r := NewRegistry(1)
	a, err := r.Create("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Create("b")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if a.State() != StateClosed || b.State() != StateClosed {
		t.Fatal("CloseAll left a session open")
	}
	if r.Len() != 0 {
		t.Fatalf("len = %d", r.Len())
	}
}

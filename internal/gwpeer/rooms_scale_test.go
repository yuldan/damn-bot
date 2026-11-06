package gwpeer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestF7GW014HostedRooms(t *testing.T) {
	t.Parallel()
	svc := &RoomService{}
	if err := checkRoomMembership(t, svc); err != nil {
		t.Fatal(err)
	}
	if _, serr := svc.Send(t.Context(), "r1", "stranger", "x"); !errors.Is(serr, ErrNotMember) {
		t.Fatalf("stranger send = %v, want ErrNotMember", serr)
	}
	if cerr := svc.Charge("r1", "agent-a", 10); cerr != nil {
		t.Fatal(cerr)
	}
	if cerr := svc.Charge("r1", "agent-b", 20); cerr != nil {
		t.Fatal(cerr)
	}
	a, aerr := svc.Cost("r1", "agent-a")
	b, berr := svc.Cost("r1", "agent-b")
	if aerr != nil || berr != nil || a != 10 || b != 20 {
		t.Fatalf("costs = %d/%d (%v/%v), want 10/20", a, b, aerr, berr)
	}
	members, merr := svc.Members("r1")
	if merr != nil || len(members) != 3 {
		t.Fatalf("members = %v, %v", members, merr)
	}
}

// checkRoomMembership admits one member by the create token and one by an
// explicit invite; a stranger and a reused token are both rejected.
func checkRoomMembership(t *testing.T, svc *RoomService) error {
	t.Helper()
	token, err := svc.Create("r1", "owner", true)
	if err != nil || token == "" {
		return fmt.Errorf("create closed room = %q: %w", token, err)
	}
	if jerr := svc.Join("r1", "stranger", ""); !errors.Is(jerr, ErrRoomClosed) {
		return fmt.Errorf("stranger join = %w", jerr)
	}
	if jerr := svc.Join("r1", "agent-a", token); jerr != nil {
		return fmt.Errorf("invited join: %w", jerr)
	}
	if jerr := svc.Join("r1", "agent-b", token); !errors.Is(jerr, ErrRoomClosed) {
		return fmt.Errorf("reused token join = %w", jerr)
	}
	token, err = svc.Invite("r1", "owner")
	if err != nil {
		return err
	}
	if jerr := svc.Join("r1", "agent-b", token); jerr != nil {
		return fmt.Errorf("second invited join: %w", jerr)
	}
	return nil
}

func TestF7GW014RoomOrder(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	svc := &RoomService{Turn: func(_ context.Context, _, _, _ string) error {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		if cur > maxSeen.Load() {
			maxSeen.Store(cur)
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
		return nil
	}}
	if _, err := svc.Create("r1", "owner", false); err != nil {
		t.Fatal(err)
	}
	const turns = 8
	var wg sync.WaitGroup
	for i := range turns {
		wg.Go(func() {
			if _, err := svc.Send(t.Context(), "r1", "owner", "turn-"+strconv.Itoa(i)); err != nil {
				t.Errorf("Send: %v", err)
			}
		})
	}
	<-entered
	close(gate)
	wg.Wait()
	if got := maxSeen.Load(); got != 1 {
		t.Errorf("simultaneous turns in one room = %d, want 1", got)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestF7GW022ScaleToZero(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	var starts, stops, delivered atomic.Int32
	s := &ScaleToZero{
		Idle: time.Minute,
		Now:  clock.Now,
		Start: func(context.Context, string) error {
			starts.Add(1)
			return nil
		},
		Stop: func(context.Context, string) error {
			stops.Add(1)
			return nil
		},
		Deliver: func(context.Context, string, *Envelope) error {
			delivered.Add(1)
			return nil
		},
	}
	if err := s.Wake(t.Context(), "p", &Envelope{ID: "m1"}); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 || delivered.Load() != 1 {
		t.Fatalf("wake: starts=%d delivered=%d", starts.Load(), delivered.Load())
	}
	clock.Advance(30 * time.Second)
	stopped, err := s.Tick(t.Context())
	if err != nil || len(stopped) != 0 {
		t.Fatalf("early tick = %v, %v", stopped, err)
	}
	s.BeginTurn("p")
	clock.Advance(2 * time.Minute)
	stopped, err = s.Tick(t.Context())
	if err != nil || len(stopped) != 0 || stops.Load() != 0 {
		t.Fatalf("tick during active turn = %v, %v, stops=%d", stopped, err, stops.Load())
	}
	s.EndTurn("p")
	clock.Advance(2 * time.Minute)
	stopped, err = s.Tick(t.Context())
	if err != nil || len(stopped) != 1 || stops.Load() != 1 {
		t.Fatalf("idle tick = %v, %v, stops=%d", stopped, err, stops.Load())
	}
	if werr := s.Wake(t.Context(), "p", &Envelope{ID: "m2"}); werr != nil {
		t.Fatal(werr)
	}
	if starts.Load() != 2 || delivered.Load() != 2 {
		t.Fatalf("wake after stop: starts=%d delivered=%d", starts.Load(), delivered.Load())
	}
}

func TestF7ScaleToZeroWakeNoLoss(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	var delivered []string
	var state State
	deliver := func(_ context.Context, _ string, env *Envelope) error {
		delivered = append(delivered, env.ID)
		return nil
	}
	s := &ScaleToZero{
		Idle: time.Minute,
		Now:  clock.Now,
		Start: func(context.Context, string) error {
			return nil
		},
		Stop: func(context.Context, string) error {
			return nil
		},
		Deliver: deliver,
		Persist: func(_ context.Context, st State) error {
			state = st
			return nil
		},
	}
	if err := s.Wake(t.Context(), "p", &Envelope{ID: "m1"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if _, err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored := &ScaleToZero{
		Idle: time.Minute,
		Now:  clock.Now,
		Start: func(context.Context, string) error {
			return nil
		},
		Stop: func(context.Context, string) error {
			return nil
		},
		Deliver: deliver,
	}
	state.Profiles = []ProfileState{{Name: "p", Pending: []Envelope{{ID: "m2"}, {ID: "m3"}}}}
	restored.Restore(state)
	if err := restored.Wake(t.Context(), "p", &Envelope{ID: "m4"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"m1", "m2", "m3", "m4"}
	if len(delivered) != len(want) {
		t.Fatalf("delivered = %v, want %v", delivered, want)
	}
	for i := range want {
		if delivered[i] != want[i] {
			t.Fatalf("delivered = %v, want %v", delivered, want)
		}
	}
	if pending := restored.Snapshot().Profiles[0].Pending; len(pending) != 0 {
		t.Fatalf("pending after wake = %v", pending)
	}
}

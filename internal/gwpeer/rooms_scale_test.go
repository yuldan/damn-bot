package gwpeer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
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

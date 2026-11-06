package gwpeer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Hosted-room errors.
var (
	ErrRoomExists   = errors.New("gwpeer: room already exists")
	ErrRoomNotFound = errors.New("gwpeer: room not found")
	ErrRoomClosed   = errors.New("gwpeer: closed room requires an invitation")
	ErrNotMember    = errors.New("gwpeer: not a room member")
)

// RoomService hosts multi-agent rooms (GW-014). Every room serializes its own
// turns, so one room never interleaves two agents, while two rooms run
// independently.
type RoomService struct {
	// Turn runs one room turn; nil records the ordered sequence only.
	Turn func(ctx context.Context, room, agent, text string) error
	// Now is the injected clock for invitation expiry; nil uses time.Now.
	Now func() time.Time

	mu    sync.Mutex
	rooms map[string]*Room
}

// Room is one hosted room. The zero value is not usable; create it through
// RoomService.Create.
type Room struct {
	ID      string
	Owner   string
	Closed  bool
	Members map[string]bool

	mu      sync.Mutex
	seq     int
	costs   map[string]int64
	invites map[string]time.Time
}

// Create makes a room. A closed room returns a one-use invitation for the
// owner to hand to the first member; a public room returns "".
func (s *RoomService) Create(id, owner string, closed bool) (string, error) {
	if id == "" || owner == "" {
		return "", fmt.Errorf("%w: id and owner are required", ErrRoomNotFound)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rooms == nil {
		s.rooms = make(map[string]*Room)
	}
	if _, ok := s.rooms[id]; ok {
		return "", fmt.Errorf("%w: %s", ErrRoomExists, id)
	}
	r := &Room{
		ID: id, Owner: owner, Closed: closed,
		Members: map[string]bool{owner: true},
		costs:   map[string]int64{},
		invites: map[string]time.Time{},
	}
	s.rooms[id] = r
	if !closed {
		return "", nil
	}
	token := NewID()
	r.invites[token] = s.now()
	return token, nil
}

// Invite mints one one-use token for the next member. Only a member may invite.
func (s *RoomService) Invite(room, by string) (string, error) {
	r, err := s.room(room)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.Members[by] {
		return "", fmt.Errorf("%w: %s", ErrNotMember, by)
	}
	token := NewID()
	r.invites[token] = s.now()
	return token, nil
}

// Join admits agent. A public room admits anyone; a closed room requires a
// token minted by Create or Invite. A duplicate join is idempotent.
func (s *RoomService) Join(room, agent, token string) error {
	r, err := s.room(room)
	if err != nil {
		return err
	}
	if agent == "" {
		return fmt.Errorf("%w: agent is required", ErrNotMember)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Members[agent] {
		return nil
	}
	if r.Closed {
		if _, ok := r.invites[token]; !ok {
			return fmt.Errorf("%w: %s", ErrRoomClosed, agent)
		}
		delete(r.invites, token)
	}
	r.Members[agent] = true
	return nil
}

// Send runs one ordered turn for a member. The room lock covers Turn, so two
// sends of one room are strictly sequential.
func (s *RoomService) Send(ctx context.Context, room, agent, text string) (int, error) {
	r, err := s.room(room)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.Members[agent] {
		return 0, fmt.Errorf("%w: %s", ErrNotMember, agent)
	}
	r.seq++
	seq := r.seq
	if s.Turn != nil {
		if err := s.Turn(ctx, room, agent, text); err != nil {
			return seq, fmt.Errorf("gwpeer: room turn: %w", err)
		}
	}
	return seq, nil
}

// Charge adds cost micros to agent's own meter; a room never pools cost.
func (s *RoomService) Charge(room, agent string, micros int64) error {
	r, err := s.room(room)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.Members[agent] {
		return fmt.Errorf("%w: %s", ErrNotMember, agent)
	}
	r.costs[agent] += micros
	return nil
}

// Cost returns agent's own accumulated cost.
func (s *RoomService) Cost(room, agent string) (int64, error) {
	r, err := s.room(room)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.costs[agent], nil
}

// Members returns the member ids, sorted.
func (s *RoomService) Members(room string) ([]string, error) {
	r, err := s.room(room)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.Members))
	for id := range r.Members {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (s *RoomService) room(id string) (*Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rooms[id]
	if r == nil {
		return nil, fmt.Errorf("%w: %s", ErrRoomNotFound, id)
	}
	return r, nil
}

func (s *RoomService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

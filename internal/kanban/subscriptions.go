package kanban

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Subscription is one persistent watcher target ("platform:chat").
type Subscription struct {
	Board   string `json:"board"`
	Target  string `json:"target"`
	AddedAt int64  `json:"added_at"`
}

func (s *Store) notifyPath() string { return filepath.Join(s.NotifyDir, ".kanban-notify.json") }

func (s *Store) loadSubs() ([]Subscription, error) {
	b, err := os.ReadFile(s.notifyPath()) // #nosec G304 -- fixed file under the operator's data dir
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read subscriptions: %w", err)
	}
	var subs []Subscription
	if err := json.Unmarshal(b, &subs); err != nil {
		return nil, fmt.Errorf("parse subscriptions: %w", err)
	}
	return subs, nil
}

func (s *Store) saveSubs(subs []Subscription) error {
	if err := os.MkdirAll(s.NotifyDir, 0o700); err != nil {
		return fmt.Errorf("subscription dir: %w", err)
	}
	b, err := json.Marshal(subs)
	if err != nil {
		return fmt.Errorf("encode subscriptions: %w", err)
	}
	tmp := s.notifyPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write subscriptions: %w", err)
	}
	if err := os.Rename(tmp, s.notifyPath()); err != nil {
		return fmt.Errorf("commit subscriptions: %w", err)
	}
	return nil
}

// Subscribe adds one target; a repeated call is a no-op.
func (s *Store) Subscribe(ctx context.Context, board, target string) (Subscription, error) {
	bid, err := s.boardRef(ctx, board)
	if err != nil {
		return Subscription{}, err
	}
	subs, err := s.loadSubs()
	if err != nil {
		return Subscription{}, err
	}
	for _, sub := range subs {
		if sub.Board == bid && sub.Target == target {
			return sub, nil
		}
	}
	sub := Subscription{Board: bid, Target: target, AddedAt: s.now().UnixMilli()}
	return sub, s.saveSubs(append(subs, sub))
}

// Notifications lists a board's targets (empty board means all).
func (s *Store) Notifications(ctx context.Context, board string) ([]Subscription, error) {
	subs, err := s.loadSubs()
	if err != nil {
		return nil, err
	}
	if board == "" || board == "default" {
		return subs, nil
	}
	bid, err := s.boardRef(ctx, board)
	if err != nil {
		return nil, err
	}
	out := subs[:0]
	for _, sub := range subs {
		if sub.Board == bid {
			out = append(out, sub)
		}
	}
	return out, nil
}

// Unsubscribe removes one target.
func (s *Store) Unsubscribe(ctx context.Context, board, target string) error {
	bid, err := s.boardRef(ctx, board)
	if err != nil {
		return err
	}
	subs, err := s.loadSubs()
	if err != nil {
		return err
	}
	out, found := subs[:0], false
	for _, sub := range subs {
		if sub.Board == bid && sub.Target == target {
			found = true
			continue
		}
		out = append(out, sub)
	}
	if !found {
		return fmt.Errorf("%w: subscription %s", ErrNotFound, target)
	}
	return s.saveSubs(out)
}

func (s *Store) boardRef(ctx context.Context, ref string) (string, error) {
	boards, err := s.Boards(ctx)
	if err != nil {
		return "", err
	}
	for _, b := range boards {
		if b.ID == ref || b.Name == ref {
			return b.ID, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrBoardNotFound, ref)
}

package kanban

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gateway/render"
)

// Sender is the gateway's rendered-message seam. Notifier never sends a raw
// fmt string: it builds markdown, runs the platform renderer, and hands the
// renderer's messages to Sender.
type Sender interface {
	Send(ctx context.Context, platform, chat string, messages []render.Message) error
}

// Subscription is one persistent watcher target.
type Subscription struct {
	Board   string    `json:"board"`
	Target  string    `json:"target"`
	AddedAt time.Time `json:"added_at"`
}

// Notifier watches task changes and sends a rate-limited (1/min/task) message
// through the platform renderer (AUTO-019).
type Notifier struct {
	Store  *Store
	Sender Sender
	mu     sync.Mutex
	last   map[string]time.Time
}

// Notify sends one change to every subscription of the board. The message is
// rendered with render.RenderTelegram (plain text + entities), so a title
// containing markdown never reaches a platform raw.
func (n *Notifier) Notify(ctx context.Context, t *Task, change string) error {
	if n.Sender == nil {
		return fmt.Errorf("%w: notifier sender", ErrNotConfigured)
	}
	n.mu.Lock()
	if n.last == nil {
		n.last = map[string]time.Time{}
	}
	now := n.Store.now()
	if prev, ok := n.last[t.ID]; ok && now.Sub(prev) < time.Minute {
		n.mu.Unlock()
		return nil
	}
	n.last[t.ID] = now
	n.mu.Unlock()
	subs, err := n.Store.Notifications(ctx, t.BoardID)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}
	md := "**kanban** `" + t.ID + "` " + string(t.Status) + "\n\n" + escapeInline(t.Title)
	if change != "" {
		md += "\n\n" + change
	}
	msgs := render.RenderTelegram(md, render.Limits{MaxLen: 3800, MaxChunks: 2})
	parts := strings.SplitN(subs[0].Target, ":", 2)
	platform, chat := "telegram", subs[0].Target
	if len(parts) == 2 {
		platform, chat = parts[0], parts[1]
	}
	if err := n.Sender.Send(ctx, platform, chat, msgs); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return nil
}

func escapeInline(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}

func (s *Store) notifyPath() string {
	return filepath.Join(s.NotifyDir, ".kanban-notify.json")
}

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
	if s.NotifyDir == "" {
		return fmt.Errorf("%w: no subscription directory", ErrInvalid)
	}
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

// Subscribe adds one target (idempotent). Target is "platform:chat".
func (s *Store) Subscribe(ctx context.Context, board, target string) (Subscription, error) {
	if strings.TrimSpace(target) == "" {
		return Subscription{}, fmt.Errorf("%w: notification target is required", ErrInvalid)
	}
	bid, err := s.resolveBoard(ctx, board)
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
	sub := Subscription{Board: bid, Target: target, AddedAt: s.now().UTC()}
	subs = append(subs, sub)
	if err := s.saveSubs(subs); err != nil {
		return Subscription{}, err
	}
	return sub, nil
}

// Notifications lists the board's subscriptions.
func (s *Store) Notifications(ctx context.Context, board string) ([]Subscription, error) {
	subs, err := s.loadSubs()
	if err != nil {
		return nil, err
	}
	want := ""
	if board != "" && board != "default" {
		if want, err = s.resolveBoard(ctx, board); err != nil {
			return nil, err
		}
	}
	var out []Subscription
	for _, sub := range subs {
		if want == "" || sub.Board == want {
			out = append(out, sub)
		}
	}
	return out, nil
}

// Unsubscribe removes one target.
func (s *Store) Unsubscribe(ctx context.Context, board, target string) error {
	subs, err := s.loadSubs()
	if err != nil {
		return err
	}
	bid, err := s.resolveBoard(ctx, board)
	if err != nil {
		return err
	}
	out := subs[:0]
	found := false
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
	if err := s.saveSubs(out); err != nil {
		return fmt.Errorf("save subscriptions: %w", err)
	}
	return nil
}

func (s *Store) resolveBoard(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		ref = "default"
	}
	var id string
	boards, err := s.Boards(ctx)
	if err != nil {
		return "", err
	}
	for _, b := range boards {
		if b.ID == ref || b.Name == ref {
			id = b.ID
			break
		}
	}
	if id == "" {
		return "", fmt.Errorf("%w: %s", ErrBoardNotFound, ref)
	}
	return id, nil
}

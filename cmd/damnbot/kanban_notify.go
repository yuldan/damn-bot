//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/kanban"
)

// Sender is the gateway's delivery seam. The notifier never sends a fmt
// string: it builds markdown and hands that body to Sender, which enqueues it
// in the gateway outbox where the platform renderer runs once.
type Sender interface {
	Send(ctx context.Context, platform, chat, key, body string) error
}

// Subscription is one persistent watcher target.
type Subscription struct {
	Board   string `json:"board"`
	Target  string `json:"target"`
	AddedAt int64  `json:"added_at"`
}

// Notifier watches task changes and sends a rate-limited message through the
// platform renderer (AUTO-019).
type Notifier struct {
	Store  *kanban.Store
	Sender Sender
	mu     sync.Mutex
	last   map[string]time.Time
}

// Notify sends one change to every subscription of the board.
func (n *Notifier) Notify(ctx context.Context, t *kanban.Task, change string) error {
	if n.Sender == nil {
		return errors.New("kanban: notifier sender")
	}
	n.mu.Lock()
	if n.last == nil {
		n.last = map[string]time.Time{}
	}
	now := time.Now()
	if n.Store.Now != nil {
		now = n.Store.Now()
	}
	if prev, ok := n.last[t.ID]; ok && now.Sub(prev) < time.Minute {
		n.mu.Unlock()
		return nil
	}
	n.last[t.ID] = now
	n.mu.Unlock()
	subs, err := n.subscriptions(ctx, t.BoardID)
	if err != nil || len(subs) == 0 {
		return err
	}
	md := "**kanban** `" + t.ID + "` " + string(t.Status) + "\n\n" + escapeInline(t.Title)
	if change != "" {
		md += "\n\n" + change
	}
	platform, chat := "telegram", subs[0].Target
	if p, c, ok := strings.Cut(subs[0].Target, ":"); ok {
		platform, chat = p, c
	}
	key := fmt.Sprintf("kanban:%s:%d", t.ID, now.UnixNano())
	if serr := n.Sender.Send(ctx, platform, chat, key, md); serr != nil {
		return fmt.Errorf("notify: %w", serr)
	}
	return nil
}

func escapeInline(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "`", "'"), "\r", " "), "\n", " ")
}

func (n *Notifier) notifyPath() string {
	return filepath.Join(n.Store.NotifyDir, ".kanban-notify.json")
}

func (n *Notifier) subscriptions(ctx context.Context, board string) ([]Subscription, error) {
	b, err := os.ReadFile(n.notifyPath()) // #nosec G304 -- fixed file under the operator's data dir
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read subscriptions: %w", err)
	}
	var subs []Subscription
	if uerr := json.Unmarshal(b, &subs); uerr != nil {
		return nil, fmt.Errorf("parse subscriptions: %w", uerr)
	}
	want := board
	boards, berr := n.Store.Boards(ctx)
	if berr != nil {
		return nil, fmt.Errorf("read boards: %w", berr)
	}
	for _, b := range boards {
		if b.Name == board || b.ID == board {
			want = b.ID
		}
	}
	var out []Subscription
	for _, sub := range subs {
		if sub.Board == want {
			out = append(out, sub)
		}
	}
	return out, nil
}

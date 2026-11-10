package builtin

import (
	"fmt"
	"sync"
	"time"
)

// Achievement is one local gamification event (PLUG-019). It is recorded
// in-process from turn/tool events and never calls a provider.
type Achievement struct {
	ID    string
	Title string
	At    time.Time
}

// ErrUnknownAchievement rejects an id outside the closed set.
var ErrUnknownAchievement = fmt.Errorf("%w: achievement", ErrUnknown)

func achievementTitle(id string) (string, bool) {
	switch id {
	case "first_turn":
		return "First turn", true
	case "first_tool":
		return "First tool call", true
	case "streak_7":
		return "Seven-day streak", true
	case "tool_explorer":
		return "Ten distinct tools", true
	}
	return "", false
}

// Achievements is a bounded, local achievement recorder. It has no provider
// dependency, so recording an event can never cause an LLM call.
type Achievements struct {
	mu     sync.Mutex
	now    func() time.Time
	events []Achievement
	seen   map[string]bool
	max    int
}

// NewAchievements returns a recorder capped at 100 events.
func NewAchievements(now func() time.Time) *Achievements {
	if now == nil {
		now = time.Now
	}
	return &Achievements{now: now, seen: map[string]bool{}, max: 100}
}

// Record unlocks id once. A duplicate is ignored; an unknown id is rejected.
func (a *Achievements) Record(id string) (Achievement, bool, error) {
	title, ok := achievementTitle(id)
	if !ok {
		return Achievement{}, false, fmt.Errorf("%w %q", ErrUnknownAchievement, id)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen[id] {
		return Achievement{}, false, nil
	}
	if len(a.events) >= a.max {
		return Achievement{}, false, fmt.Errorf("%w: recorder full", ErrUnknownAchievement)
	}
	a.seen[id] = true
	ev := Achievement{ID: id, Title: title, At: a.now().UTC()}
	a.events = append(a.events, ev)
	return ev, true, nil
}

// List returns a copy of the unlocked achievements in unlock order.
func (a *Achievements) List() []Achievement {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Achievement(nil), a.events...)
}

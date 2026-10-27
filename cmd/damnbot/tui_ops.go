// TUI session/action/clipboard seam (A4): real ctxdb/journal work behind the
// internal/tui interfaces, which stay I/O free (E-09).
package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/tui"
)

// tuiActions implements tui.Sessions over the runner's lazy store.
type tuiActions struct {
	db        func(context.Context) (*ctxdb.Store, error)
	cfg       *config.Config
	configDir string
	modelRef  string
	active    func(string) bool
}

func (a *tuiActions) New(ctx context.Context) (tui.SessionChange, error) {
	db, err := a.db(ctx)
	if err != nil {
		return tui.SessionChange{}, err
	}
	sid, _, err := newRunSession(ctx, db, a.modelRef, "", "tui")
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("new session: %w", err)
	}
	return tui.SessionChange{SessionID: sid, Detail: "new session " + sid}, nil
}

// Fork branches at the current head (SPEC §4.6); the parent is untouched.
func (a *tuiActions) Fork(ctx context.Context, sessionID string) (tui.SessionChange, error) {
	db, err := a.db(ctx)
	if err != nil {
		return tui.SessionChange{}, err
	}
	head, err := db.Head(ctx, sessionID)
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("fork: %w", err)
	}
	child, err := db.Fork(ctx, sessionID, head)
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("fork: %w", err)
	}
	return tui.SessionChange{SessionID: child, Detail: fmt.Sprintf("forked %s at seq %d -> %s", sessionID, head, child)}, nil
}

// Rewind is a fork at an earlier seq/point (SPEC §4.6); the old branch stays.
func (a *tuiActions) Rewind(ctx context.Context, sessionID, at string) (tui.SessionChange, error) {
	db, err := a.db(ctx)
	if err != nil {
		return tui.SessionChange{}, err
	}
	target, _, err := branchPoint(ctx, db, sessionID, at, true)
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("rewind: %w", err)
	}
	child, err := db.Fork(ctx, sessionID, target)
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("rewind: %w", err)
	}
	return tui.SessionChange{SessionID: child, Detail: fmt.Sprintf("rewound %s at seq %d -> %s", sessionID, target, child)}, nil
}

// Undo runs the existing journal path (lease, conflict, retention, sandbox).
func (a *tuiActions) Undo(ctx context.Context, sessionID, target string) (string, error) {
	if a.active != nil && a.active(sessionID) {
		return "", errors.New("a turn is running; cancel it first")
	}
	db, err := a.db(ctx)
	if err != nil {
		return "", err
	}
	store, err := journalstore.New(db, nil)
	if err != nil {
		return "", fmt.Errorf("undo: %w", err)
	}
	o := &undoOptions{session: sessionID, count: 1}
	if target != "" {
		id, perr := strconv.ParseInt(strings.TrimPrefix(target, "a"), 10, 64)
		if perr != nil || id <= 0 || !strings.HasPrefix(target, "a") {
			return "", fmt.Errorf("invalid action %q (want a<id>)", target)
		}
		o.action = id
	}
	var out strings.Builder
	code, uerr := undoApply(ctx, &cliEnv{cfg: *a.cfg, configDir: a.configDir, db: db}, store, o, &out)
	detail := strings.TrimSpace(out.String())
	switch {
	case uerr != nil:
		return detail, uerr
	case code != 0:
		return detail, errors.New(detail)
	}
	return detail, nil
}

// List returns the newest visible sessions with the metadata the view shows.
func (a *tuiActions) List(ctx context.Context) ([]tui.SessionInfo, error) {
	db, err := a.db(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := db.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	if len(ids) > 50 {
		ids = ids[:50]
	}
	out := make([]tui.SessionInfo, 0, len(ids))
	for _, id := range ids {
		cwd, model, xerr := sessionMeta(ctx, db, id)
		if xerr != nil {
			continue
		}
		out = append(out, tui.SessionInfo{ID: id, Cwd: cwd, Model: model})
	}
	return out, nil
}

// clipboardAdapter is the production tui.Clipboard; a missing platform utility
// is an error the TUI shows verbatim.
type clipboardAdapter struct{}

func (clipboardAdapter) Copy(text string) error {
	if err := clipboard.WriteAll(text); err != nil {
		return fmt.Errorf("system clipboard: %w", err)
	}
	return nil
}

package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Session label keys of SPEC §11.2 session_labels. Values are append-only:
// the highest id wins, so "0" clears a flag without deleting history.
const (
	LabelTitle    = "title"
	LabelArchived = "archived"
	LabelPinned   = "pinned"
)

// MaxTitleRunes bounds a session title (SPEC §10 `sessions rename`).
const MaxTitleRunes = 120

// SessionRow is one session metadata row: the immutable session row plus the
// last title/archived/pinned labels and the event count.
type SessionRow struct {
	ID, ParentID     string
	Origin, Cwd      string
	Model, Title     string
	CreatedAt        int64
	Events           int64
	Archived, Pinned bool
}

// SessionRows returns every visible session, newest first. Archived sessions
// are included; callers filter on SessionRow.Archived.
func (s *Store) SessionRows(ctx context.Context) ([]SessionRow, error) {
	var out []SessionRow
	err := s.each(ctx, `
		SELECT s.id, coalesce(s.parent_id, ''), s.origin, s.cwd, s.model, s.created_at,
		       coalesce((SELECT l.value FROM session_labels l
		                  WHERE l.session_id = s.id AND l.key = 'title'
		                  ORDER BY l.id DESC LIMIT 1), ''),
		       coalesce((SELECT l.value FROM session_labels l
		                  WHERE l.session_id = s.id AND l.key = 'archived'
		                  ORDER BY l.id DESC LIMIT 1), ''),
		       coalesce((SELECT l.value FROM session_labels l
		                  WHERE l.session_id = s.id AND l.key = 'pinned'
		                  ORDER BY l.id DESC LIMIT 1), ''),
		       (SELECT count(*) FROM events e WHERE e.session_id = s.id)
		  FROM sessions s
		 WHERE s.id <> ? AND `+notPurging("s.id")+`
		 ORDER BY s.created_at DESC, s.id`, []any{SystemSession}, func(r *sql.Rows) error {
		var si SessionRow
		var archived, pinned string
		if err := r.Scan(&si.ID, &si.ParentID, &si.Origin, &si.Cwd, &si.Model, &si.CreatedAt,
			&si.Title, &archived, &pinned, &si.Events); err != nil {
			return wrap(err)
		}
		si.Archived, si.Pinned = flagOn(archived), flagOn(pinned)
		out = append(out, si)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// flagOn reads an archived/pinned label; empty and "0" are off.
func flagOn(v string) bool { return v != "" && v != "0" }

// SetSessionLabel appends one label value. Title is sanitized and bounded;
// archived/pinned accept only "0"/"1".
func (s *Store) SetSessionLabel(ctx context.Context, sid, key, value string) error {
	value, err := labelValue(key, value)
	if err != nil {
		return err
	}
	return s.Write(ctx, func(t *Tx) error {
		var n int
		if err := t.Get(`SELECT count(*) FROM sessions s WHERE s.id = ? AND `+notPurging("s.id"), []any{sid}, &n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("ctxdb: session %s: %w", sid, ErrNotFound)
		}
		_, err := t.Exec(`INSERT INTO session_labels(session_id, key, value, created_at) VALUES (?,?,?,?)`,
			sid, key, value, t.s.clk.WallMs())
		return wrap(err)
	})
}

// SessionLabel returns the current value of one label; ok is false when unset.
func (s *Store) SessionLabel(ctx context.Context, sid, key string) (value string, ok bool, err error) {
	if verr := validLabelKey(key); verr != nil {
		return "", false, verr
	}
	err = s.get(ctx, `SELECT value FROM session_labels WHERE session_id = ? AND key = ? ORDER BY id DESC LIMIT 1`,
		[]any{sid, key}, &value)
	if err == nil {
		return value, true, nil
	}
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	return "", false, err
}

// labelValue validates a label and normalizes title text.
func labelValue(key, value string) (string, error) {
	if err := validLabelKey(key); err != nil {
		return "", err
	}
	switch key {
	case LabelArchived, LabelPinned:
		if value != "0" && value != "1" {
			return "", fmt.Errorf("ctxdb: label %s: value %q wants 0 or 1", key, value)
		}
		return value, nil
	default:
		title := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || r == utf8.RuneError {
				return -1
			}
			return r
		}, value)
		title = strings.TrimSpace(title)
		if utf8.RuneCountInString(title) > MaxTitleRunes {
			r := []rune(title)
			title = strings.TrimSpace(string(r[:MaxTitleRunes]))
		}
		return title, nil
	}
}

// validLabelKey restricts writes to the user-facing labels; gateway_chat and
// purge_state are owned by their subsystems.
func validLabelKey(key string) error {
	switch key {
	case LabelTitle, LabelArchived, LabelPinned:
		return nil
	}
	return fmt.Errorf("ctxdb: label key %q is not writable", key)
}

// PurgeCandidates exposes the §11.6 P computation without starting a purge
// (`sessions purge --dry-run`).
func (s *Store) PurgeCandidates(ctx context.Context, req PurgeRequest) (PurgeReport, error) {
	return s.purgeSet(ctx, req)
}

// SessionsOlderThan returns visible sessions created before the cutoff in
// milliseconds (`sessions prune --archive`); oldest first.
func (s *Store) SessionsOlderThan(ctx context.Context, before int64) ([]string, error) {
	return s.strings(ctx, `SELECT s.id FROM sessions s WHERE s.id <> ? AND s.created_at < ? AND `+
		notPurging("s.id")+` ORDER BY s.created_at, s.id`, SystemSession, before)
}

// Vacuum repacks the database after a purge (SPEC §10 `sessions purge`).
func (s *Store) Vacuum(ctx context.Context) error {
	if _, err := s.w.ExecContext(ctx, "VACUUM"); err != nil {
		return wrap(err)
	}
	return nil
}

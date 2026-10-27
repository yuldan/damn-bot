package importstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"

	_ "modernc.org/sqlite"
)

// MaxMessages bounds one source session; the rest is reported by the caller.
const MaxMessages = 20000

// ErrInvalid marks a state.db that is not a Hermes session database.
var ErrInvalid = errors.New("importstore: invalid hermes state.db")

// HermesMessage is one source transcript row (role and content as stored).
type HermesMessage struct {
	SourceID, Timestamp     int64
	Role, Content, ToolName string
}

// HermesSession is one source session with messages in timestamp/id order.
type HermesSession struct {
	SourceID, Title, Model, Cwd string
	Messages                    []HermesMessage
	Skipped                     int // messages beyond MaxMessages
}

// ReadHermes reads every session of a private copy of state.db; an unreadable
// or unknown schema is ErrInvalid.
func ReadHermes(ctx context.Context, path string) (sessions []HermesSession, err error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("state.db: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer func() { err = errors.Join(err, db.Close()) }()
	if _, perr := db.ExecContext(ctx, "PRAGMA query_only=ON"); perr != nil {
		return nil, fmt.Errorf("state.db: %w", perr)
	}
	records, err := query(ctx, db, "SELECT * FROM sessions", []string{"id"})
	if err != nil {
		return nil, err
	}
	if _, merr := query(ctx, db, "SELECT * FROM messages LIMIT 0",
		[]string{"id", "session_id", "role", "content"}); merr != nil {
		return nil, merr
	}
	for _, rec := range records {
		s := HermesSession{SourceID: text(rec["id"]), Model: text(rec["model"]), Cwd: text(rec["cwd"]), Title: text(rec["title"])}
		if s.SourceID == "" {
			continue
		}
		if s.Messages, s.Skipped, err = messages(ctx, db, s.SourceID); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func messages(ctx context.Context, db *sql.DB, sessionID string) ([]HermesMessage, int, error) {
	records, err := query(ctx, db,
		"SELECT * FROM messages WHERE session_id = ? ORDER BY id LIMIT ?",
		[]string{"id", "session_id", "role", "content"}, sessionID, MaxMessages+1)
	if err != nil {
		return nil, 0, err
	}
	msgs := make([]HermesMessage, 0, len(records))
	for _, rec := range records {
		msgs = append(msgs, HermesMessage{
			SourceID: int64Value(rec["id"]), Timestamp: epochMillis(rec["timestamp"], rec["created_at"]),
			Role: text(rec["role"]), Content: text(rec["content"]), ToolName: text(rec["tool_name"]),
		})
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].Timestamp != msgs[j].Timestamp {
			return msgs[i].Timestamp < msgs[j].Timestamp
		}
		return msgs[i].SourceID < msgs[j].SourceID
	})
	skipped := 0
	if len(msgs) > MaxMessages {
		skipped = len(msgs) - MaxMessages
		msgs = msgs[:MaxMessages]
		var total int
		if qerr := db.QueryRowContext(ctx, "SELECT count(*) FROM messages WHERE session_id = ?", sessionID).
			Scan(&total); qerr == nil && total > len(msgs) {
			skipped = total - len(msgs)
		}
	}
	return msgs, skipped, nil
}

func query(ctx context.Context, db *sql.DB, q string, required []string, args ...any) (out []map[string]any, err error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("state.db columns: %w", err)
	}
	for _, name := range required {
		if !slices.Contains(cols, name) {
			return nil, fmt.Errorf("%w: missing column %s", ErrInvalid, name)
		}
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("state.db row: %w", err)
		}
		rec := make(map[string]any, len(cols))
		for i, name := range cols {
			if raw, ok := vals[i].([]byte); ok {
				rec[name] = string(raw)
			} else {
				rec[name] = vals[i]
			}
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state.db rows: %w", err)
	}
	return out, nil
}

func text(v any) string {
	if v == nil {
		return ""
	}
	if raw, ok := v.([]byte); ok {
		return string(raw)
	}
	return fmt.Sprint(v)
}

func int64Value(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	}
	return 0
}

func epochMillis(values ...any) int64 {
	for _, v := range values {
		if ms := toMillis(v); ms > 0 {
			return ms
		}
	}
	return 0
}

func toMillis(v any) int64 {
	switch t := v.(type) {
	case float64:
		return scaleSeconds(t)
	case int64:
		if t <= 0 {
			return 0
		}
		if t < 1e12 {
			return t * 1000
		}
		return t
	}
	return 0
}

func scaleSeconds(f float64) int64 {
	if f <= 0 {
		return 0
	}
	if f < 1e11 {
		return int64(f * 1000)
	}
	return int64(f)
}

// Persisted-session seam of the ACP server: session/load replays the stored
// transcript (SPEC §13.3) and a failed prompt reports the turn_end event id.
// The store is opened per operation so the ACP server never holds a second
// long-lived writer beside the session's lazy run store.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// acpReplayItem is one transcript update replayed by session/load.
type acpReplayItem struct {
	Update     string // sessionUpdate variant
	Text       string
	ToolCallID string
	Status     string // ACP tool-call status for tool_call_update
}

// acpHistory is the persisted-session seam used by session/load and by the
// provider-error response (data.event_id). It is nil when no store is wired.
type acpHistory interface {
	Load(ctx context.Context, sid string) ([]acpReplayItem, error)
	TurnEndEventID(ctx context.Context, sid string) (int64, error)
}

// acpHistoryError carries the closed error code reported in
// data.error_code (SPEC §13.3, §11.10).
type acpHistoryError struct {
	code string
	err  error
}

func (e *acpHistoryError) Error() string { return e.code + ": " + e.err.Error() }
func (e *acpHistoryError) Unwrap() error { return e.err }

// ctxHistoryStore reads sessions from the run store. Every method opens and
// closes its own handle.
type ctxHistoryStore struct {
	cfg       *config.Config
	dataDir   string
	configDir string
}

// withStore opens the run store, calls fn, and closes it. Opening is the
// same path the surfaces use, so a corrupt, newer, or encrypted-without-key
// database fails here with a closed code instead of a fabricated history.
func (h *ctxHistoryStore) withStore(ctx context.Context, fn func(*ctxdb.Store) error) error {
	codec, err := runCodec(ctx, h.cfg, h.dataDir, h.configDir)
	if err != nil {
		return &acpHistoryError{code: "encrypted_db", err: err}
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: h.dataDir, Kind: "acp", Codec: codec})
	if err != nil {
		return &acpHistoryError{code: historyCode(err), err: err}
	}
	ferr := fn(db)
	if cerr := db.CloseContext(ctx); cerr != nil && ferr == nil {
		ferr = &acpHistoryError{code: "internal", err: cerr}
	}
	return ferr
}

// Load returns the fork-aware transcript of sid in replay order. A missing
// or purged session is ctxdb.ErrNotFound; a session leased by another live
// process is ctxdb.ErrSessionBusy. It never creates a session.
func (h *ctxHistoryStore) Load(ctx context.Context, sid string) ([]acpReplayItem, error) {
	var items []acpReplayItem
	err := h.withStore(ctx, func(db *ctxdb.Store) error {
		if _, err := db.SessionInfo(ctx, sid); err != nil {
			return fmt.Errorf("session %s: %w", sid, err)
		}
		if _, err := db.AcquireLease(ctx, sid); err != nil {
			return fmt.Errorf("lease %s: %w", sid, err)
		}
		if err := db.ReleaseLease(ctx, sid); err != nil {
			return fmt.Errorf("release lease %s: %w", sid, err)
		}
		rows, err := db.History(ctx, sid, 0, ctxdb.WithHistoryLimit(ctxdb.DefaultHistoryLimit))
		if err != nil {
			return fmt.Errorf("history %s: %w", sid, err)
		}
		items = replayItems(rows)
		return nil
	})
	if err != nil {
		if errors.Is(err, ctxdb.ErrNotFound) || errors.Is(err, ctxdb.ErrSessionBusy) {
			return nil, err
		}
		if _, ok := errors.AsType[*acpHistoryError](err); ok {
			return nil, err
		}
		return nil, &acpHistoryError{code: historyCode(err), err: err}
	}
	return items, nil
}

// TurnEndEventID returns the id of the newest turn_end event of sid (0 when
// none is stored). It is the event_id of a provider_error response.
func (h *ctxHistoryStore) TurnEndEventID(ctx context.Context, sid string) (int64, error) {
	var id int64
	err := h.withStore(ctx, func(db *ctxdb.Store) error {
		return db.Write(ctx, func(tx *ctxdb.Tx) error {
			err := tx.Get(`SELECT id FROM events WHERE session_id = ?1 AND kind = 'turn_end'
				ORDER BY seq DESC LIMIT 1`, []any{sid}, &id)
			if errors.Is(err, ctxdb.ErrNotFound) {
				return nil
			}
			return fmt.Errorf("turn_end %s: %w", sid, err)
		})
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// replayItems maps stored events to ACP session/update variants. Assistant
// output marked partial is skipped exactly like history replay (EC-PROV-008).
func replayItems(rows []ctxdb.Event) []acpReplayItem {
	out := make([]acpReplayItem, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		switch r.Kind {
		case "user_message":
			if r.Content != "" {
				out = append(out, acpReplayItem{Update: "user_message_chunk", Text: r.Content})
			}
		case "assistant_message":
			if r.Content == "" || skipAssistant(r) {
				continue
			}
			out = append(out, acpReplayItem{Update: "agent_message_chunk", Text: r.Content})
		case "tool_result":
			if r.ToolCallID == "" {
				continue
			}
			out = append(out, acpReplayItem{
				Update: "tool_call_update", Text: r.Content,
				ToolCallID: r.ToolCallID, Status: replayToolStatus(r.Payload),
			})
		}
	}
	return out
}

// replayToolStatus maps a stored result payload to the ACP status vocabulary.
func replayToolStatus(payload string) string {
	var p struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err == nil && p.Status == "failed" {
		return "failed"
	}
	return "completed"
}

// historyCode maps a store error to its closed SPEC §11.10 code.
func historyCode(err error) string {
	switch {
	case errors.Is(err, ctxdb.ErrMigrationFailed):
		return "migration_failed"
	case errors.Is(err, ctxdb.ErrMigrationRefused):
		return "migration_refused"
	case errors.Is(err, ctxdb.ErrSchemaNewer):
		return "schema_newer"
	case errors.Is(err, ctxdb.ErrFTSCorrupt):
		return "fts_corrupt"
	case errors.Is(err, ctxdb.ErrEncrypted):
		return "encrypted_db"
	case errors.Is(err, ctxdb.ErrAtRestPlaintext):
		return "at_rest_existing_plaintext"
	case errors.Is(err, ctxdb.ErrReadOnly):
		return "read_only"
	case errors.Is(err, ctxdb.ErrDBBusy):
		return "db_busy"
	case errors.Is(err, ctxdb.ErrDiskFull):
		return "disk_full"
	case errors.Is(err, ctxdb.ErrSessionBusy):
		return "session_busy"
	}
	return "internal"
}

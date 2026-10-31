// Package mcpstore is the ctxdb storage of MCP tool pins (SEC-04,
// GAP-SEC-04; ADR-272). The table is declared in migration 0017; this package
// stores only the sha256 of a canonical tool definition plus a diff summary,
// never the raw schema.
//
// The client side is a small interface in internal/mcp (E-09); this package
// declares its own value types so it does not import a module package (the
// pattern of ctxdb/gwstore, ADR-074), and cmd/damnbot converts between them.
package mcpstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// State is the closed pin state (migration 0017 CHECK).
type State string

// Pin states.
const (
	StatePinned      State = "pinned"
	StateChanged     State = "changed"
	StateQuarantined State = "quarantined"
)

// ErrInvalidPin is a malformed pin row.
var ErrInvalidPin = errors.New("mcpstore: invalid pin")

// Pin is one mcp_tool_pins row.
type Pin struct {
	Server       string
	Tool         string
	SchemaSHA256 string
	FirstSeen    int64
	ApprovedAt   int64
	ApprovedBy   string
	State        State
	Diff         string
}

// Store is MCP pin storage over one ctxdb store.
type Store struct{ DB *ctxdb.Store }

const pinColumns = `server_id, tool_name, schema_sha256, first_seen, coalesce(approved_at, 0),
	approved_by, state, diff_summary`

func validPin(p *Pin) error {
	if p.Server == "" || p.Tool == "" || len(p.Server) > 128 || len(p.Tool) > 128 {
		return fmt.Errorf("%w: server or tool", ErrInvalidPin)
	}
	if len(p.SchemaSHA256) != 64 {
		return fmt.Errorf("%w: schema sha256 length", ErrInvalidPin)
	}
	if p.State != StatePinned && p.State != StateChanged && p.State != StateQuarantined {
		return fmt.Errorf("%w: state %q", ErrInvalidPin, p.State)
	}
	if p.FirstSeen <= 0 || p.ApprovedAt < 0 {
		return fmt.Errorf("%w: timestamps", ErrInvalidPin)
	}
	if !json.Valid([]byte(p.Diff)) {
		return fmt.Errorf("%w: diff summary is not JSON", ErrInvalidPin)
	}
	return nil
}

// Pin returns one pin and whether the row exists.
func (s Store) Pin(ctx context.Context, server, tool string) (Pin, bool, error) {
	var p Pin
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var found bool
		var xerr error
		p, found, xerr = getPin(tx, `WHERE server_id = ?1 AND tool_name = ?2`, server, tool)
		if xerr != nil {
			return xerr
		}
		if !found {
			return fmt.Errorf("%w: %s/%s", ctxdb.ErrNotFound, server, tool)
		}
		return nil
	})
	switch {
	case errors.Is(err, ctxdb.ErrNotFound):
		return Pin{}, false, nil
	case err != nil:
		return Pin{}, false, fmt.Errorf("mcpstore pin: %w", err)
	}
	return p, true, nil
}

// Pins returns every pin of server, ordered by tool name.
func (s Store) Pins(ctx context.Context, server string) ([]Pin, error) {
	var out []Pin
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		names, xerr := tx.Strings(`SELECT tool_name FROM mcp_tool_pins WHERE server_id = ?1 ORDER BY tool_name`, server)
		if xerr != nil {
			return fmt.Errorf("list tools: %w", xerr)
		}
		out = out[:0]
		for _, name := range names {
			p, found, gerr := getPin(tx, `WHERE server_id = ?1 AND tool_name = ?2`, server, name)
			if gerr != nil {
				return gerr
			}
			if found {
				out = append(out, p)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mcpstore pins: %w", err)
	}
	return out, nil
}

func getPin(tx *ctxdb.Tx, where string, args ...any) (Pin, bool, error) {
	var p Pin
	err := tx.Get(`SELECT `+pinColumns+` FROM mcp_tool_pins `+where, args,
		&p.Server, &p.Tool, &p.SchemaSHA256, &p.FirstSeen, &p.ApprovedAt, &p.ApprovedBy, &p.State, &p.Diff)
	switch {
	case errors.Is(err, ctxdb.ErrNotFound):
		return Pin{}, false, nil
	case err != nil:
		return Pin{}, false, fmt.Errorf("read pin: %w", err)
	}
	return p, true, nil
}

// PutPin inserts or replaces one pin; an existing first_seen is never reset.
func (s Store) PutPin(ctx context.Context, p Pin) error { //nolint:gocritic // value API mirrors the client pin
	if err := validPin(&p); err != nil {
		return err
	}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO mcp_tool_pins
			(server_id, tool_name, schema_sha256, first_seen, approved_at, approved_by, state, diff_summary, updated_at)
			VALUES (?1, ?2, ?3, ?4, NULLIF(?5, 0), ?6, ?7, ?8, ?4)
			ON CONFLICT(server_id, tool_name) DO UPDATE SET
				schema_sha256 = excluded.schema_sha256,
				approved_at = coalesce(excluded.approved_at, mcp_tool_pins.approved_at),
				approved_by = excluded.approved_by,
				state = excluded.state,
				diff_summary = excluded.diff_summary,
				updated_at = excluded.updated_at`,
			p.Server, p.Tool, p.SchemaSHA256, p.FirstSeen, p.ApprovedAt, p.ApprovedBy, p.State, p.Diff)
		if xerr != nil {
			return fmt.Errorf("put: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("mcpstore put pin: %w", err)
	}
	return nil
}

// ApprovePin pins digest only while it is still the observed definition.
func (s Store) ApprovePin(ctx context.Context, server, tool, digest, by string, at int64) (bool, error) {
	if server == "" || tool == "" || len(digest) != 64 || by == "" || at <= 0 {
		return false, fmt.Errorf("%w: approval fields", ErrInvalidPin)
	}
	diff, err := json.Marshal(map[string]string{"change": "approved", "sha256": digest})
	if err != nil {
		return false, fmt.Errorf("%w: diff: %w", ErrInvalidPin, err)
	}
	var updated int64
	err = s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		n, xerr := tx.Exec(`UPDATE mcp_tool_pins
			SET state = 'pinned', approved_at = ?4, approved_by = ?5, diff_summary = ?6, updated_at = ?4
			WHERE server_id = ?1 AND tool_name = ?2 AND schema_sha256 = ?3
				AND state IN ('changed', 'quarantined')`,
			server, tool, digest, at, by, string(diff))
		updated = n
		if xerr != nil {
			return fmt.Errorf("update: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("mcpstore approve pin: %w", err)
	}
	return updated == 1, nil
}

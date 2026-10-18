package ctxdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ToolCall is the journal record written with tool_dispatch before Run.
type ToolCall struct {
	CallID, Name, Class, ArgsSha string // Class: read|idempotent|non_idempotent
	Target, BeforeSha, AfterSha  string // file tools (hash comparison, not refs)
	Pgid                         int    // exec process group (AE-05)
	PgidStart                    string
}

// jsonOf encodes values built from maps/slices of strings and numbers,
// which cannot fail to marshal.
func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// StartTurn inserts journal `running` + turn_start in one transaction.
func (t *Tx) StartTurn(sid, turn string) error {
	now := t.s.clk.WallMs()
	if _, err := t.Exec(`INSERT INTO turn_journal(turn_id, session_id, state, started_at, updated_at) VALUES (?,?,'running',?,?)`,
		turn, sid, now, now); err != nil {
		return err
	}
	_, err := t.AppendEvent(sid, &Event{Kind: "turn_start", TurnID: turn})
	return err
}

// ToolPending appends tool_dispatch and moves the journal to tool_pending.
func (t *Tx) ToolPending(sid, turn string, c *ToolCall) error {
	if _, err := t.AppendEvent(sid, &Event{Kind: "tool_dispatch", TurnID: turn, ToolCallID: c.CallID, ToolName: c.Name}); err != nil {
		return err
	}
	var pg any
	if c.Pgid > 0 {
		pg = c.Pgid
	}
	_, err := t.Exec(`UPDATE turn_journal SET state = 'tool_pending', last_tool_call_id = ?, last_tool_name = ?, tool_class = ?, args_sha = ?,
		target_path = ?, before_sha = ?, after_sha = ?, tool_pgid = ?, tool_pgid_start = ?, updated_at = ? WHERE turn_id = ?`,
		c.CallID, c.Name, c.Class, c.ArgsSha, nullable(c.Target), nullable(c.BeforeSha), nullable(c.AfterSha), pg, nullable(c.PgidStart),
		t.s.clk.WallMs(), turn)
	return err
}

// EndTurn appends turn_end and deletes the journal row.
func (t *Tx) EndTurn(sid, turn, stop string) error {
	if _, err := t.AppendEvent(sid, &Event{Kind: "turn_end", TurnID: turn, Payload: jsonOf(map[string]string{"stop_reason": stop})}); err != nil {
		return err
	}
	_, err := t.Exec("DELETE FROM turn_journal WHERE turn_id = ?", turn)
	return err
}

// Recovered is the outcome of recovering one open journal row.
type Recovered struct {
	Session, Turn, Status string
	Decision              string // auto_continue | deferred | ask | restore
}

type openJournal struct {
	turn, sid, state, origin  string
	call, name, class, target string
	before, after, pgidStart  string
	pgid                      int
	lease                     liveRec // PID 0 = no lease row
}

// RecoverJournals is SELF-HEALING §3.4 step 4 / §11.11.8: open journals of
// unlabelled sessions whose lease is takeover_ok (case (a) only unless
// allowB, the background scanner) are closed with observed results.
func (s *Store) RecoverJournals(ctx context.Context, allowB bool) ([]Recovered, error) {
	js, err := s.openJournals(ctx)
	var out []Recovered
	for i := 0; err == nil && i < len(js); i++ {
		j := &js[i]
		if _, mine := s.token(j.sid); j.lease.PID != 0 && (mine || !s.takeoverOK("lease:"+j.sid, j.lease, allowB)) {
			continue
		}
		if j.pgid > 0 && (j.lease.PID == 0 || j.lease.Host == s.id.Host) {
			killGroup(j.pgid, j.pgidStart) // before recovery (AE-05, AF-07)
		}
		var r Recovered
		werr := s.Write(ctx, func(t *Tx) (rerr error) { r, rerr = t.recoverOne(j, allowB); return rerr })
		switch {
		case errors.Is(werr, ErrSessionBusy) || errors.Is(werr, ErrSessionPurging) || errors.Is(werr, ErrNotFound):
			continue // lost the race, or labelled meanwhile (I3)
		case werr != nil:
			err = werr
		case r.Decision != "auto_continue":
			err = s.ReleaseLease(ctx, j.sid)
		}
		out = append(out, r)
	}
	return out, err
}

func (s *Store) openJournals(ctx context.Context) ([]openJournal, error) {
	var out []openJournal
	err := s.each(ctx, s.q["journal.open"], nil, func(r *sql.Rows) error {
		var j openJournal
		x := &j.lease
		if err := r.Scan(&j.turn, &j.sid, &j.state, &j.origin, &j.call, &j.name, &j.class, &j.target, &j.before, &j.after,
			&j.pgidStart, &j.pgid, &x.PID, &x.Start, &x.Host, &x.BootID, &x.HeartbeatAt, &x.ExpiresAt); err != nil {
			return wrap(err)
		}
		out = append(out, j)
		return nil
	})
	return out, err
}

// recoverOne applies the SELF-HEALING §3.4 table to one journal under lease.
func (t *Tx) recoverOne(j *openJournal, allowB bool) (Recovered, error) {
	r := Recovered{Session: j.sid, Turn: j.turn, Decision: "auto_continue"}
	if _, err := t.AcquireLease(j.sid, allowB); err != nil {
		return r, err
	}
	var state string
	if err := t.Get("SELECT state FROM turn_journal WHERE turn_id = ?", []any{j.turn}, &state); err != nil {
		return r, err
	}
	if state != j.state {
		return r, fmt.Errorf("ctxdb: journal %s changed: %w", j.turn, ErrNotFound)
	}
	if j.state == "tool_pending" {
		r.Status, r.Decision = pendingOutcome(j)
		if _, err := t.AppendEvent(j.sid, &Event{
			Kind: "tool_result", TurnID: j.turn, ToolCallID: j.call, ToolName: j.name,
			Payload: jsonOf(map[string]any{"status": r.Status, "retry_safe": r.Status != "outcome_unknown", "by": "recovery"}),
		}); err != nil {
			return r, err
		}
	}
	if r.Decision == "auto_continue" && (frontendOf(j.origin) != t.s.opt.Kind || j.origin == "cron") {
		r.Decision = "deferred"
	}
	return r, t.closeCrashed(j.sid, j.turn, j.name, r.Decision)
}

// closeCrashed writes turn_end{crashed} + crash_recovered and parks the row.
func (t *Tx) closeCrashed(sid, turn, tool, decision string) error {
	if _, err := t.AppendEvent(sid, &Event{Kind: "turn_end", TurnID: turn, Payload: `{"stop_reason":"crashed"}`}); err != nil {
		return err
	}
	if _, err := t.AppendEvent(sid, &Event{
		Kind: "crash_recovered", TurnID: turn,
		Payload: jsonOf(map[string]string{"turn_id": turn, "last_tool": tool, "decision": decision}),
	}); err != nil {
		return err
	}
	next := map[string]string{"auto_continue": "resumed", "ask": "awaiting_confirmation"}[decision]
	if next == "" {
		next = "crashed"
	}
	_, err := t.Exec("UPDATE turn_journal SET state = ?, updated_at = ? WHERE turn_id = ?", next, t.s.clk.WallMs(), turn)
	return err
}

func pendingOutcome(j *openJournal) (status, decision string) {
	switch j.class {
	case "read":
		return "interrupted", "auto_continue"
	case "idempotent":
		switch fileSha(j.target) {
		case j.after:
			return "applied_verified", "auto_continue"
		case j.before:
			return "not_applied", "auto_continue"
		}
		return "conflict", "ask"
	}
	return "outcome_unknown", "ask"
}

// fileSha hashes the tool's target file ("" when unreadable). The read goes
// through an os.Root of the file's directory (no symlink escape upward).
func fileSha(p string) string {
	root, err := os.OpenRoot(filepath.Dir(p))
	if err != nil {
		return ""
	}
	b, err := root.ReadFile(filepath.Base(p))
	if root.Close() != nil || err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// frontendOf maps a session origin to the frontend allowed to auto-continue.
func frontendOf(origin string) string {
	switch origin {
	case "tui", "acp":
		return origin
	case "gateway", "cron", "web", "kanban", "delegate", "batch":
		return "serve"
	}
	return ""
}

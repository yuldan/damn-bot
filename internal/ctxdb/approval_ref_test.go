package ctxdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// ApprovalRequest is turn_journal.approval_request (§11.11.10).
type ApprovalRequest struct {
	RequestID  string   `json:"request_id"`
	Class      string   `json:"class"` // dangerous classes: owner only
	Dangerous  bool     `json:"dangerous"`
	PreviewSha string   `json:"preview_sha"`
	Channels   []string `json:"channels"`
	ExpiresAt  int64    `json:"expires_at"` // wall UTC ms (only wall-clock use)
	ArgsSha    string   `json:"args_sha,omitempty"`
	ClientKey  string   `json:"client_key,omitempty"`
}

// ApprovalResponse is one row of the approval_responses inbox.
type ApprovalResponse struct {
	SessionID, TurnID, RequestID, PreviewSha string
	Decision, EditedArgs, Source, Responder  string
	Token                                    string // mcp_serve_tokens.id_hash for source mcp_serve
}

// RequestApproval parks the turn in awaiting_approval (holder of the lease).
func (t *Tx) RequestApproval(turn string, r ApprovalRequest) error {
	_, err := t.Exec("UPDATE turn_journal SET state = 'awaiting_approval', approval_request = ?, updated_at = ? WHERE turn_id = ?",
		jsonOf(r), t.s.clk.WallMs(), turn)
	return err
}

// RespondApproval is the responder side (no lease): the DB rejects a second
// answer (UNIQUE) and answers for a request that is not pending
// (approval_not_pending) — late or stale answers are never stored.
func (s *Store) RespondApproval(ctx context.Context, r ApprovalResponse) error {
	return s.Write(ctx, func(t *Tx) error {
		_, err := t.Exec(`INSERT INTO approval_responses(session_id, turn_id, request_id, preview_sha, decision, edited_args, source, responder, responder_token, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, r.SessionID, r.TurnID, r.RequestID, r.PreviewSha, r.Decision, nullable(r.EditedArgs), r.Source,
			r.Responder, nullable(r.Token), s.clk.WallMs())
		return err
	})
}

// ApprovalDecision is what the consumer recorded.
type ApprovalDecision struct {
	Decision, Reason string
	NewRequestID     string // remote edit → new request (never executed directly)
}

// ConsumeApproval is the consumer (lease holder) decision transaction:
// re-validate authority, token, channel, preview and expiry; failures are a
// recorded deny (fail-closed); remote edit becomes a new request; the
// response row is deleted in the same transaction (exactly once, I15).
func (s *Store) ConsumeApproval(ctx context.Context, sid, turn, owner string, validEdit func(string) bool) (ApprovalDecision, error) {
	var d ApprovalDecision
	err := s.Write(ctx, func(t *Tx) error {
		var raw string
		if err := t.Get("SELECT approval_request FROM turn_journal WHERE turn_id = ? AND state = 'awaiting_approval'", []any{turn}, &raw); err != nil {
			return err
		}
		var req ApprovalRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			return fmt.Errorf("ctxdb: approval_request: %w", err)
		}
		var r ApprovalResponse
		var id int64
		var edited, token *string
		err := t.Get(`SELECT id, preview_sha, decision, edited_args, source, responder, responder_token FROM approval_responses WHERE request_id = ?`,
			[]any{req.RequestID}, &id, &r.PreviewSha, &r.Decision, &edited, &r.Source, &r.Responder, &token)
		switch {
		case errors.Is(err, ErrNotFound) && s.clk.WallMs() >= req.ExpiresAt:
			d = ApprovalDecision{Decision: "timeout", Reason: "expired"}
			return t.recordApproval(sid, turn, req, r, d)
		case err != nil:
			return err
		}
		r.EditedArgs, r.Token = deref(edited), deref(token)
		d = t.judge(req, r, owner, validEdit)
		if _, err := t.Exec("DELETE FROM approval_responses WHERE id = ?", id); err != nil {
			return err
		}
		return t.recordApproval(sid, turn, req, r, d)
	})
	return d, err
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// judge re-validates a response (§11.11.10 authority table).
func (t *Tx) judge(req ApprovalRequest, r ApprovalResponse, owner string, validEdit func(string) bool) ApprovalDecision {
	deny := func(reason string) ApprovalDecision { return ApprovalDecision{Decision: "deny", Reason: reason} }
	if r.Source == "mcp_serve" {
		var scope, identity string
		var revoked *int64
		err := t.Get("SELECT scope, identity, revoked_at FROM mcp_serve_tokens WHERE id_hash = ?", []any{r.Token}, &scope, &identity, &revoked)
		if err != nil || scope != "approve" || revoked != nil || identity != r.Responder {
			return deny("scope")
		}
	}
	switch {
	case req.Dangerous && r.Responder != owner:
		return deny("unauthorized")
	case !slices.Contains(req.Channels, r.Source) && r.Responder != owner:
		return deny("unauthorized")
	case r.PreviewSha != req.PreviewSha:
		return deny("preview_mismatch")
	case t.s.clk.WallMs() >= req.ExpiresAt:
		return deny("expired")
	case r.Decision == "edit" && (validEdit == nil || !validEdit(r.EditedArgs)):
		return deny("invalid_edit")
	case r.Decision == "edit":
		return ApprovalDecision{Decision: "edit", Reason: "superseded", NewRequestID: newID()}
	}
	return ApprovalDecision{Decision: r.Decision}
}

// recordApproval appends the `approval` event + approvals row and moves the
// journal out of awaiting_approval (or onto the new request after an edit).
func (t *Tx) recordApproval(sid, turn string, req ApprovalRequest, r ApprovalResponse, d ApprovalDecision) error {
	ev, err := t.AppendEvent(sid, Event{Kind: "approval", TurnID: turn, Payload: jsonOf(map[string]any{
		"request_id": req.RequestID, "decision": d.Decision, "reason": d.Reason, "cross_channel": r.Source != "" && !slices.Contains(req.Channels, r.Source)})})
	if err != nil {
		return err
	}
	src := r.Source
	if src == "" {
		src = "recovery"
	}
	if _, err := t.Exec(`INSERT INTO approvals(session_id, event_id, tool_name, subject, decision, source, created_at) VALUES (?,?,?,?,?,?,?)`,
		sid, ev, req.Class, req.RequestID, d.Decision, src, t.s.clk.WallMs()); err != nil {
		return err
	}
	if d.Decision == "edit" {
		req.RequestID = d.NewRequestID
		return t.RequestApproval(turn, req)
	}
	_, err = t.Exec("UPDATE turn_journal SET state = 'running', approval_request = NULL, updated_at = ? WHERE turn_id = ?", t.s.clk.WallMs(), turn)
	return err
}

// CancelApproval is the mcp serve cancel path: deny{cancelled} recorded in
// the decision transaction before any delivery exists (§11.11.10).
func (s *Store) CancelApproval(ctx context.Context, sid, turn string) error {
	return s.Write(ctx, func(t *Tx) error {
		var raw string
		if err := t.Get("SELECT approval_request FROM turn_journal WHERE turn_id = ? AND state = 'awaiting_approval'", []any{turn}, &raw); err != nil {
			return err
		}
		var req ApprovalRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			return fmt.Errorf("ctxdb: approval_request: %w", err)
		}
		return t.recordApproval(sid, turn, req, ApprovalResponse{}, ApprovalDecision{Decision: "deny", Reason: "cancelled"})
	})
}

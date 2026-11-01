// Package mcpservestore is the ctxdb storage of `damnbot mcp serve` client
// tokens and the cross-process approval inbox (SPEC §11.9, §11.11.10;
// GAP-SEC-21). Only a sha256 of a token is stored: the value is shown once at
// creation and never again, so a database copy cannot mint a bearer.
//
// The package declares its own value types and does not import internal/mcp
// (the ctxdb/consumer pattern of ADR-074); cmd/damnbot converts.
package mcpservestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Scopes of a serve token (migration 0007 CHECK).
const (
	ScopeRead    = "read"
	ScopeSend    = "send"
	ScopeApprove = "approve"
)

var (
	// ErrInvalidToken is a missing, revoked, or profile-mismatched token.
	ErrInvalidToken = errors.New("mcpservestore: invalid token")
	// ErrInvalidTokenRow is a malformed token row.
	ErrInvalidTokenRow = errors.New("mcpservestore: invalid token row")
	// ErrNotPending is an approval for a request that is not waiting.
	ErrNotPending = errors.New("approval_not_pending")
	// ErrForbidden is a responder without authority for that request.
	ErrForbidden = errors.New("mcpservestore: forbidden")
	// ErrInvalidApproval is a malformed inbox request.
	ErrInvalidApproval = errors.New("mcpservestore: invalid approval")
)

// P26 classes can never be answered by a remote responder (ADR-084).
var p26Classes = map[string]bool{
	"irreversible": true, "monetary": true, "delegated_agent_bypass": true,
}

// Token is one mcp_serve_tokens row; IDHash is sha256(token).
type Token struct {
	IDHash    string
	Profile   string
	Identity  string
	Scope     string
	CreatedAt int64
	RevokedAt int64
}

// Hash is the only representation of a token the store ever sees.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Store is MCP-serve storage over one ctxdb store.
type Store struct{ DB *ctxdb.Store }

func validScope(scope string) bool {
	return scope == ScopeRead || scope == ScopeSend || scope == ScopeApprove
}

func validToken(t *Token) error {
	if len(t.IDHash) != 64 || t.Profile == "" || t.Identity == "" || len(t.Profile) > 128 || len(t.Identity) > 128 {
		return fmt.Errorf("%w: identity or hash", ErrInvalidTokenRow)
	}
	if !validScope(t.Scope) {
		return fmt.Errorf("%w: scope %q", ErrInvalidTokenRow, t.Scope)
	}
	if t.CreatedAt <= 0 || t.RevokedAt < 0 {
		return fmt.Errorf("%w: timestamps", ErrInvalidTokenRow)
	}
	if strings.ContainsAny(t.Identity, "\x00\r\n\x1f") {
		return fmt.Errorf("%w: identity", ErrInvalidTokenRow)
	}
	return nil
}

// Create inserts a token row. A duplicate hash is an error: a token is never
// silently re-issued.
func (s Store) Create(ctx context.Context, t Token) error {
	if err := validToken(&t); err != nil {
		return err
	}
	return s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, err := tx.Exec(`INSERT INTO mcp_serve_tokens(id_hash, profile, identity, scope, created_at, revoked_at)
			VALUES (?1, ?2, ?3, ?4, ?5, NULL)`, t.IDHash, t.Profile, t.Identity, t.Scope, t.CreatedAt)
		if err != nil {
			return fmt.Errorf("mcpservestore: create token: %w", err)
		}
		return nil
	})
}

// Authenticate returns the live token row for rawToken in profile. Any
// mismatch (unknown hash, revoked row, another profile) is one error, so a
// caller cannot distinguish "wrong token" from "revoked token".
func (s Store) Authenticate(ctx context.Context, rawToken, profile string) (Token, error) {
	if rawToken == "" || len(rawToken) > 512 || profile == "" {
		return Token{}, ErrInvalidToken
	}
	var t Token
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT id_hash, profile, identity, scope, created_at, coalesce(revoked_at, 0)
			FROM mcp_serve_tokens WHERE id_hash = ?1 AND profile = ?2 AND revoked_at IS NULL`,
			[]any{Hash(rawToken), profile},
			&t.IDHash, &t.Profile, &t.Identity, &t.Scope, &t.CreatedAt, &t.RevokedAt)
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return Token{}, ErrInvalidToken
	}
	if err != nil {
		return Token{}, fmt.Errorf("mcpservestore: authenticate: %w", err)
	}
	return t, nil
}

// Revoke marks a token dead. It returns false when the row was already
// revoked, so `revoke` can be idempotent without claiming a second revoke.
func (s Store) Revoke(ctx context.Context, idHash string, at int64) (bool, error) {
	if len(idHash) != 64 {
		return false, fmt.Errorf("%w: hash", ErrInvalidTokenRow)
	}
	changed := false
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		n, xerr := tx.Exec(`UPDATE mcp_serve_tokens SET revoked_at = ?2 WHERE id_hash = ?1 AND revoked_at IS NULL`, idHash, at)
		if xerr != nil {
			return xerr
		}
		changed = n > 0
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("mcpservestore: revoke: %w", err)
	}
	return changed, nil
}

// List returns the tokens of one profile, hashes only.
func (s Store) List(ctx context.Context, profile string) ([]Token, error) {
	out := []Token{}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		rows, xerr := tx.Strings(`SELECT id_hash || char(31) || profile || char(31) || identity || char(31) ||
			scope || char(31) || created_at || char(31) || coalesce(revoked_at, 0)
			FROM mcp_serve_tokens WHERE profile = ?1 ORDER BY created_at`, profile)
		if xerr != nil {
			return xerr
		}
		for _, row := range rows {
			f := strings.Split(row, "\x1f")
			if len(f) != 6 {
				return fmt.Errorf("%w: row shape", ErrInvalidTokenRow)
			}
			out = append(out, Token{
				IDHash: f[0], Profile: f[1], Identity: f[2], Scope: f[3],
				CreatedAt: parseInt(f[4]), RevokedAt: parseInt(f[5]),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mcpservestore: list: %w", err)
	}
	return out, nil
}

// approvalPayload is the JSON stored in turn_journal.approval_request.
type approvalPayload struct {
	RequestID  string   `json:"request_id"`
	Tool       string   `json:"tool"`
	Class      string   `json:"class"`
	Origin     string   `json:"origin"`
	PreviewSHA string   `json:"preview_sha"`
	Channels   []string `json:"channels"`
	ExpiresAt  int64    `json:"expires_at"`
}

func parseApproval(raw string) (approvalPayload, error) {
	var p approvalPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return approvalPayload{}, fmt.Errorf("%w: approval_request is not JSON", ErrInvalidApproval)
	}
	if p.RequestID == "" || p.Tool == "" || len(p.RequestID) > 128 || len(p.Tool) > 128 {
		return approvalPayload{}, fmt.Errorf("%w: request id or tool", ErrInvalidApproval)
	}
	return p, nil
}

// OpenApproval is one pending request of the inbox.
type OpenApproval struct {
	RequestID  string   `json:"request_id"`
	TurnID     string   `json:"turn_id"`
	SessionID  string   `json:"session_id"`
	Tool       string   `json:"tool"`
	Class      string   `json:"class"`
	Origin     string   `json:"origin"`
	PreviewSHA string   `json:"preview_sha"`
	Channels   []string `json:"channels,omitempty"`
	ExpiresAt  int64    `json:"expires_at"`
}

// OpenApprovals lists the journal rows waiting for a decision, newest first.
func (s Store) OpenApprovals(ctx context.Context, limit int) ([]OpenApproval, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []OpenApproval{}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		rows, xerr := tx.Strings(`SELECT session_id || char(31) || turn_id FROM turn_journal
			WHERE state = 'awaiting_approval' ORDER BY updated_at DESC LIMIT ?1`, limit)
		if xerr != nil {
			return xerr
		}
		for _, row := range rows {
			session, turn, ok := strings.Cut(row, "\x1f")
			if !ok {
				return fmt.Errorf("%w: journal row shape", ErrInvalidApproval)
			}
			var raw string
			if err := tx.Get(`SELECT approval_request FROM turn_journal WHERE turn_id = ?1 AND session_id = ?2`,
				[]any{turn, session}, &raw); err != nil {
				return err
			}
			p, perr := parseApproval(raw)
			if perr != nil {
				return perr
			}
			out = append(out, OpenApproval{
				RequestID: p.RequestID, TurnID: turn, SessionID: session, Tool: p.Tool,
				Class: p.Class, Origin: p.Origin, PreviewSHA: p.PreviewSHA, Channels: p.Channels, ExpiresAt: p.ExpiresAt,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mcpservestore: open approvals: %w", err)
	}
	return out, nil
}

// RespondRequest is one decision submitted by a remote responder. Session
// and turn are resolved from the journal row: the caller cannot choose them.
type RespondRequest struct {
	RequestID          string
	Decision           string // allow_once|deny
	PreviewSHA         string
	Responder          string
	ResponderTokenHash string
	Now                int64
}

// Respond validates authority and inserts the inbox row. The DB trigger also
// refuses a request that is not waiting, so a stale or duplicate answer can
// never be consumed.
func (s Store) Respond(ctx context.Context, r RespondRequest) (OpenApproval, error) {
	if r.RequestID == "" || len(r.RequestID) > 128 || r.Responder == "" || len(r.Responder) > 128 {
		return OpenApproval{}, fmt.Errorf("%w: request or responder", ErrInvalidApproval)
	}
	if len(r.ResponderTokenHash) != 64 {
		return OpenApproval{}, fmt.Errorf("%w: responder token", ErrInvalidApproval)
	}
	if r.Decision != "allow_once" && r.Decision != "deny" {
		// `edit` is never valid for mcp_serve (SPEC §11.11.10 CHECK).
		return OpenApproval{}, fmt.Errorf("%w: decision %q", ErrForbidden, r.Decision)
	}
	var out OpenApproval
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var session, turn, raw string
		err := tx.Get(`SELECT session_id, turn_id, approval_request FROM turn_journal
			WHERE state = 'awaiting_approval' AND json_extract(approval_request, '$.request_id') = ?1`,
			[]any{r.RequestID}, &session, &turn, &raw)
		if errors.Is(err, ctxdb.ErrNotFound) {
			return ErrNotPending
		}
		if err != nil {
			return err
		}
		p, perr := parseApproval(raw)
		if perr != nil {
			return perr
		}
		switch {
		case p.ExpiresAt != 0 && p.ExpiresAt <= r.Now:
			return ErrNotPending
		case p.PreviewSHA == "" || p.PreviewSHA != r.PreviewSHA:
			return fmt.Errorf("%w: preview sha", ErrForbidden)
		case p.Origin == "" || p.Origin == "mcp_serve":
			return fmt.Errorf("%w: origin %q", ErrForbidden, p.Origin)
		case p26Classes[p.Class]:
			return fmt.Errorf("%w: class %q", ErrForbidden, p.Class)
		}
		_, xerr := tx.Exec(`INSERT INTO approval_responses(session_id, turn_id, request_id, preview_sha,
			decision, edited_args, source, responder, responder_token, created_at)
			VALUES (?1, ?2, ?3, ?4, ?5, NULL, 'mcp_serve', ?6, ?7, ?8)`,
			session, turn, p.RequestID, r.PreviewSHA, r.Decision, r.Responder, r.ResponderTokenHash, r.Now)
		if xerr != nil {
			return classifyInsert(xerr)
		}
		out = OpenApproval{
			RequestID: p.RequestID, TurnID: turn, SessionID: session, Tool: p.Tool,
			Class: p.Class, Origin: p.Origin, PreviewSHA: p.PreviewSHA, Channels: p.Channels, ExpiresAt: p.ExpiresAt,
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrNotPending), errors.Is(err, ErrForbidden), errors.Is(err, ErrInvalidApproval):
			return OpenApproval{}, err
		case errors.Is(err, ctxdb.ErrSessionPurging):
			return OpenApproval{}, ErrNotPending
		default:
			return OpenApproval{}, fmt.Errorf("mcpservestore: respond: %w", err)
		}
	}
	return out, nil
}

func parseInt(s string) int64 {
	var n int64
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int64(s[i]-'0')
	}
	return n
}

// classifyInsert maps the DB guard messages to the closed error set.
func classifyInsert(err error) error {
	text := err.Error()
	switch {
	case strings.Contains(text, "approval_not_pending"), strings.Contains(text, "UNIQUE constraint"):
		return ErrNotPending
	case strings.Contains(text, "session_purging"):
		return ErrNotPending
	default:
		return err
	}
}

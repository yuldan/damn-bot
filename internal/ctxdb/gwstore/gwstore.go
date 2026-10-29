// Package gwstore is the ctxdb storage of the F4 gateway core (SPEC §11.2,
// §9.7; ADR-074, ADR-245). It reads gateway_identities, persists one
// gateway_inbound row per platform update with the primary-key dedupe of
// SPEC §9.7, and resolves the routed session of a gateway conversation.
//
// The queue tables, guards and indexes are declared in migration 0003 (plus
// payload_enc from 0009); migration 0015 adds gateway_inbound.thread_id,
// gateway_message_ids and gateway_callbacks (ADR-249). The consumer side is a small
// interface in internal/gateway (E-09); this package declares its own value
// types so it does not import that package (the pattern of
// ctxdb/limitstore, ADR-074), and cmd/damnbot converts between the two.
package gwstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Store is gateway storage over one ctxdb store.
type Store struct{ DB *ctxdb.Store }

// Identity is one gateway_identities row.
type Identity struct {
	DisplayName string
	Role        string // owner|user
	Status      string // pending|allowlisted|blocked
	Strength    string // cryptographic|platform_authenticated|weak (V-13)
	// Workspace is the stored sandbox root of a role user identity; "" means
	// the derived default (ADR-084).
	Workspace string
}

// Inbound is one normalised platform update. UpdateID is the platform's
// stable update/message id; (Platform, UpdateID) is the dedupe key. ThreadID is
// the platform topic ("" without one) so a restarted worker routes the queued
// row to the same session; Payload is the JSON content kept with the queue row
// (ADR-046).
type Inbound struct {
	Platform string
	ChatID   string
	ThreadID string
	SenderID string
	UpdateID string
	Payload  string
}

// Route identifies one gateway conversation (SPEC §9.7): sessions are per
// (platform, chat, thread).
type Route struct {
	Platform string
	ChatID   string
	ThreadID string
}

// ErrInvalidInbound rejects an update the storage invariants cannot accept.
// The row is not written.
var ErrInvalidInbound = errors.New("gwstore: invalid inbound")

// Lookup returns the identity row of (platform, externalID). found is false
// when the platform never saw that id; callers decide the policy (the gateway
// denies it, SPEC §9.7).
func (s Store) Lookup(ctx context.Context, platform, externalID string) (Identity, bool, error) {
	var (
		id                                             Identity
		displayName, role, status, strength, workspace string
	)
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT display_name, role, status, strength, coalesce(workspace, '')
			FROM gateway_identities WHERE platform = ?1 AND external_id = ?2`,
			[]any{platform, externalID}, &displayName, &role, &status, &strength, &workspace)
	})
	switch {
	case errors.Is(err, ctxdb.ErrNotFound):
		return Identity{}, false, nil
	case err != nil:
		return Identity{}, false, fmt.Errorf("gwstore lookup: %w", err)
	}
	id = Identity{DisplayName: displayName, Role: role, Status: status, Strength: strength, Workspace: workspace}
	return id, true, nil
}

// Record stores in once and reports whether this call inserted it. A duplicate
// returns false and leaves the original row (and its content) untouched, so
// the caller performs no second action for a replayed update. Payload is
// sealed with the at-rest codec and contributes blob_refs like any other
// content column.
func (s Store) Record(ctx context.Context, in *Inbound) (bool, error) {
	if err := in.validate(); err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	owner := in.Platform + ":" + in.UpdateID
	recorded := false
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		sealed, stored, err := tx.SealRow("gateway_inbound.payload", owner, in.Payload, "{}")
		if err != nil {
			return fmt.Errorf("seal payload: %w", err)
		}
		ids, err := tx.Strings(`INSERT INTO gateway_inbound(platform, external_id, chat_id, thread_id, sender_id, payload, payload_enc, received_at)
			VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)
			ON CONFLICT(platform, external_id) DO NOTHING
			RETURNING external_id`,
			in.Platform, in.UpdateID, in.ChatID, in.ThreadID, in.SenderID, stored, nullable(sealed), now)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidInbound, err)
		}
		if len(ids) == 0 {
			return nil // replay: original row and content kept
		}
		refs, err := ctxdb.BlobRefsOf("gateway_inbound", owner, nil, map[string]string{"payload": in.Payload})
		if err != nil {
			return fmt.Errorf("blob refs: %w", err)
		}
		if err := tx.InsertRefs(ctxdb.SystemSession, now, refs); err != nil {
			return fmt.Errorf("insert blob refs: %w", err)
		}
		recorded = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("gwstore record: %w", err)
	}
	return recorded, nil
}

// Session returns the routed session of route that principal may read: the
// newest gateway_conversations row whose session is not purging and whose
// gateway_chat label is empty or equals principal. A session labelled with
// another principal is invisible (SPEC §9.7 isolation, ADR-084). A missing
// route or a route with only foreign sessions returns found=false.
func (s Store) Session(ctx context.Context, route Route, principal string) (string, bool, error) {
	var sid string
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT c.session_id FROM gateway_conversations c
			WHERE c.platform = ?1 AND c.chat_id = ?2 AND c.thread_id = ?3
			  AND NOT EXISTS (SELECT 1 FROM session_labels l
			        WHERE l.session_id = c.session_id AND l.key = 'purge_state')
			  AND coalesce((SELECT l.value FROM session_labels l
			        WHERE l.session_id = c.session_id AND l.key = 'gateway_chat'
			        ORDER BY l.id DESC LIMIT 1), '') IN ('', ?4)
			ORDER BY c.created_at DESC, c.rowid DESC LIMIT 1`,
			[]any{route.Platform, route.ChatID, route.ThreadID, principal}, &sid)
	})
	switch {
	case errors.Is(err, ctxdb.ErrNotFound):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("gwstore session: %w", err)
	}
	return sid, true, nil
}

// validate enforces the inbound transport invariants the queue row needs. The
// JSON check happens before sealing because an encrypted payload stores a
// placeholder in the CHECK-constrained column.
func (in *Inbound) validate() error {
	for _, f := range [...]struct{ name, value string }{
		{"platform", in.Platform},
		{"chat", in.ChatID},
		{"sender", in.SenderID},
		{"update", in.UpdateID},
	} {
		if f.value == "" {
			return fmt.Errorf("%w: %s id is required", ErrInvalidInbound, f.name)
		}
	}
	if !json.Valid([]byte(in.Payload)) {
		return fmt.Errorf("%w: payload is not JSON", ErrInvalidInbound)
	}
	return nil
}

func nullable(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

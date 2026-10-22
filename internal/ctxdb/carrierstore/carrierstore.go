// Package carrierstore persists opaque reasoning carriers and their
// per-session signature rejections (GAP-PROV-01, SPEC §4.1/§11.9, ADR-131).
// It is a consumer store (ADR-074): it imports only ctxdb, keeps the wire
// bytes byte-identical, and lets the provider/assemble layer decide which
// carrier is replayed to which route. Both tables are append-only; purge is
// the ctxdb ladder's job.
package carrierstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// ErrCarrier rejects an invalid carrier, rejection, or scope.
var ErrCarrier = errors.New("carrierstore: invalid carrier")

// Size bounds (SEC8-5): one opaque block and the carriers of one assistant
// message. Oversized input is refused rather than inflating the DB and every
// later request.
const (
	MaxCarrierBytes = 256 << 10
	MaxMessageBytes = 1 << 20
)

// Kind is reasoning_carriers.kind (SPEC §11.9, closed set).
type Kind string

// Carrier kinds of the provider protocols.
const (
	KindThoughtSignature   Kind = "thought_signature"
	KindThinking           Kind = "thinking"
	KindRedactedThinking   Kind = "redacted_thinking"
	KindEncryptedReasoning Kind = "encrypted_reasoning"
	KindReasoningOpaque    Kind = "reasoning_opaque"
)

// Valid reports whether k is a SPEC §11.9 kind.
func (k Kind) Valid() bool {
	switch k {
	case KindThoughtSignature, KindThinking, KindRedactedThinking, KindEncryptedReasoning, KindReasoningOpaque:
		return true
	}
	return false
}

// Carrier is one wire block to persist for an assistant message. Payload is
// stored byte-identically (no JSON re-encoding); Replayable is false for a
// plaintext carrier whose text was redacted (it is dropped, not replayed).
type Carrier struct {
	Ord        int
	Family     string // provider family of the route that produced it
	Model      string
	Kind       Kind
	Anchor     string // tool_call_id or part index
	Payload    []byte
	Replayable bool
	CreatedAt  int64 // unix ms, supplied by the caller
}

// Ref is the per-carrier digest an assistant event payload records
// (`carriers:[{ord,sha}]`), so the hash chain covers the carrier bytes.
type Ref struct {
	Ord int    `json:"ord"`
	SHA string `json:"sha"`
}

// Stored is one persisted carrier, including bytes for replay.
type Stored struct {
	ID         int64
	EventID    int64
	Ord        int
	Family     string
	Model      string
	Kind       Kind
	Anchor     string
	Payload    []byte
	SHA        string
	Replayable bool
	CreatedAt  int64
}

// Store is the carrier storage over one ctxdb database.
type Store struct{ DB *ctxdb.Store }

// PayloadSHA is the hex sha256 recorded in payload_sha and the event payload.
func PayloadSHA(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Refs returns the digest refs of cs without touching the database, so the
// caller can embed them in the assistant event before appending it.
func Refs(cs []Carrier) []Ref {
	refs := make([]Ref, len(cs))
	for i, c := range cs {
		refs[i] = Ref{Ord: c.Ord, SHA: PayloadSHA(c.Payload)}
	}
	return refs
}

// Save inserts the carriers of one assistant event and returns their refs.
// It verifies that eventID belongs to sessionID (a carrier must never be
// attached to another session's event), that ord is unique and non-negative,
// that ordinary fields are sane, and that the kind is in the SPEC set.
// Re-saving the same (event, ord) is a duplicate, not a silent overwrite
// (the table is append-only).
func (s Store) Save(ctx context.Context, sessionID string, eventID int64, cs []Carrier) ([]Ref, error) {
	if sessionID == "" || eventID <= 0 {
		return nil, fmt.Errorf("%w: session %q event %d", ErrCarrier, sessionID, eventID)
	}
	if len(cs) == 0 {
		return nil, nil
	}
	if err := validateCarriers(cs); err != nil {
		return nil, err
	}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var owner string
		if gerr := tx.Get("SELECT session_id FROM events WHERE id = ?", []any{eventID}, &owner); gerr != nil {
			if errors.Is(gerr, ctxdb.ErrNotFound) {
				return fmt.Errorf("%w: event %d not found", ErrCarrier, eventID)
			}
			return fmt.Errorf("carrierstore: find event: %w", gerr)
		}
		if owner != sessionID {
			return fmt.Errorf("%w: event %d belongs to another session", ErrCarrier, eventID)
		}
		for i := range cs {
			c := &cs[i]
			if _, ierr := tx.Exec(`INSERT INTO reasoning_carriers(event_id, ord, provider_family, model, kind, anchor, payload, payload_sha, replayable, created_at)
				VALUES (?,?,?,?,?,?,?,?,?,?)`,
				eventID, c.Ord, c.Family, c.Model, string(c.Kind), c.Anchor,
				c.Payload, PayloadSHA(c.Payload), boolInt(c.Replayable), c.CreatedAt); ierr != nil {
				return fmt.Errorf("carrierstore: insert carrier %d: %w", i, ierr)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("carrierstore: save: %w", err)
	}
	return Refs(cs), nil
}

// validateCarriers checks every column and rejects a duplicate ord in the
// same assistant message (UNIQUE(event_id, ord) would otherwise fail late).
func validateCarriers(cs []Carrier) error {
	ords := make(map[int]bool, len(cs))
	total := 0
	for i := range cs {
		if err := cs[i].validate(); err != nil {
			return fmt.Errorf("%w: carrier %d: %w", ErrCarrier, i, err)
		}
		if ords[cs[i].Ord] {
			return fmt.Errorf("%w: duplicate ord %d", ErrCarrier, cs[i].Ord)
		}
		ords[cs[i].Ord] = true
		if len(cs[i].Payload) > MaxCarrierBytes {
			return fmt.Errorf("%w: carrier %d is %d bytes > %d", ErrCarrier, i, len(cs[i].Payload), MaxCarrierBytes)
		}
		total += len(cs[i].Payload)
		if total > MaxMessageBytes {
			return fmt.Errorf("%w: carriers total %d bytes > %d", ErrCarrier, total, MaxMessageBytes)
		}
	}
	return nil
}

func (c *Carrier) validate() error {
	switch {
	case c.Ord < 0:
		return fmt.Errorf("ord %d", c.Ord)
	case !plain(c.Family) || !plain(c.Model):
		return fmt.Errorf("family %q model %q", c.Family, c.Model)
	case !c.Kind.Valid():
		return fmt.Errorf("kind %q", c.Kind)
	case len(c.Payload) == 0:
		return errors.New("empty payload")
	case c.CreatedAt <= 0:
		return fmt.Errorf("created_at %d", c.CreatedAt)
	}
	if strings.ContainsRune(c.Anchor, 0) {
		return errors.New("anchor contains NUL")
	}
	return nil
}

// Load returns the carriers of the given assistant events, restricted to
// sessionID and ordered by event then ord. Events outside the session are
// simply absent; payload bytes are copies.
func (s Store) Load(ctx context.Context, sessionID string, eventIDs []int64) ([]Stored, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("%w: empty session", ErrCarrier)
	}
	if len(eventIDs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(eventIDs)+1)
	args = append(args, sessionID)
	marks := make([]string, len(eventIDs))
	for i, id := range eventIDs {
		marks[i] = "?"
		args = append(args, id)
	}
	var out []Stored
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		in := strings.Join(marks, ",")
		ids, ierr := tx.Ints(`SELECT c.id FROM reasoning_carriers c JOIN events e ON e.id = c.event_id
			WHERE e.session_id = ? AND c.event_id IN (`+in+`) ORDER BY c.event_id, c.ord`, args...)
		if ierr != nil {
			return fmt.Errorf("carrierstore: list carriers: %w", ierr)
		}
		for _, id := range ids {
			var st Stored
			var kind string
			var replay bool
			if gerr := tx.Get(`SELECT c.id, c.event_id, c.ord, c.provider_family, c.model, c.kind, c.anchor,
					c.payload, c.payload_sha, c.replayable, c.created_at
				FROM reasoning_carriers c WHERE c.id = ?`, []any{id},
				&st.ID, &st.EventID, &st.Ord, &st.Family, &st.Model, &kind,
				&st.Anchor, &st.Payload, &st.SHA, &replay, &st.CreatedAt); gerr != nil {
				return fmt.Errorf("carrierstore: read carrier %d: %w", id, gerr)
			}
			st.Kind = Kind(kind)
			st.Replayable = replay
			st.Payload = slices.Clone(st.Payload)
			out = append(out, st)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("carrierstore: load: %w", err)
	}
	return out, nil
}

// Reject records that the provider rejected these carriers' signatures for
// sessionID (400 signature invalid). The row is keyed (carrier_id, session)
// and append-only, so a second call for the same pair is a no-op; a carrier
// of another session is refused.
func (s Store) Reject(ctx context.Context, sessionID string, carrierIDs []int64, errCode string, at int64) error {
	if sessionID == "" || !plain(errCode) || at <= 0 || len(carrierIDs) == 0 {
		return fmt.Errorf("%w: reject session %q code %q at %d", ErrCarrier, sessionID, errCode, at)
	}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		for _, id := range carrierIDs {
			var owner string
			if gerr := tx.Get(`SELECT e.session_id FROM reasoning_carriers c JOIN events e ON e.id = c.event_id WHERE c.id = ?`,
				[]any{id}, &owner); gerr != nil {
				if errors.Is(gerr, ctxdb.ErrNotFound) {
					return fmt.Errorf("%w: carrier %d not found", ErrCarrier, id)
				}
				return fmt.Errorf("carrierstore: find carrier: %w", gerr)
			}
			if owner != sessionID {
				return fmt.Errorf("%w: carrier %d belongs to another session", ErrCarrier, id)
			}
			if _, ierr := tx.Exec(`INSERT OR IGNORE INTO reasoning_carrier_rejections(carrier_id, session_id, error_code, created_at)
				VALUES (?,?,?,?)`, id, sessionID, errCode, at); ierr != nil {
				return fmt.Errorf("carrierstore: insert rejection: %w", ierr)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("carrierstore: reject: %w", err)
	}
	return nil
}

// Rejected returns the carrier IDs the provider rejected for sessionID.
func (s Store) Rejected(ctx context.Context, sessionID string) (map[int64]bool, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("%w: empty session", ErrCarrier)
	}
	out := map[int64]bool{}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, ierr := tx.Ints("SELECT carrier_id FROM reasoning_carrier_rejections WHERE session_id = ?", sessionID)
		if ierr != nil {
			return fmt.Errorf("carrierstore: list rejections: %w", ierr)
		}
		for _, id := range ids {
			out[id] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("carrierstore: rejected: %w", err)
	}
	return out, nil
}

// plain reports a non-empty identifier without whitespace or control bytes.
func plain(s string) bool {
	return s != "" && len(s) <= 512 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n\t")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// BySHA returns the carrier IDs of sessionID whose payload sha256 is in
// shas, for mapping a rejected wire block back to its stored row.
func (s Store) BySHA(ctx context.Context, sessionID string, shas []string) ([]int64, error) {
	if sessionID == "" || len(shas) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(shas)+1)
	args = append(args, sessionID)
	marks := make([]string, len(shas))
	for i, sha := range shas {
		marks[i] = "?"
		args = append(args, sha)
	}
	var out []int64
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, ierr := tx.Ints(`SELECT c.id FROM reasoning_carriers c JOIN events e ON e.id = c.event_id
			WHERE e.session_id = ? AND c.payload_sha IN (`+strings.Join(marks, ",")+`) ORDER BY c.id`, args...)
		if ierr != nil {
			return fmt.Errorf("carrierstore: find by sha: %w", ierr)
		}
		out = ids
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("carrierstore: by sha: %w", err)
	}
	return out, nil
}

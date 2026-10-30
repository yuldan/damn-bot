package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway"
)

// approvalTTL bounds how long a recorded decision suppresses a re-issued
// prompt for the same session/turn/tool/preview fingerprint. It matches the
// longest interactive approval timeout (SPEC §14.1, 600 s) plus margin.
const approvalTTL = 11 * time.Minute

// approvalRecord is the durable decision for one request_id (GAP-SEC-13).
// Executed is set when the decision is returned to the dispatcher: a resumed
// turn sees it and neither prompts again nor re-executes the tool.
type approvalRecord struct {
	Decision  string `json:"decision"` // allow|deny
	NonceSHA  string `json:"nonce_sha"`
	ExpiresAt int64  `json:"expires_at"` // wall UTC ms
	Executed  bool   `json:"executed"`
}

// approvalLedger is the consumer-side storage seam (E-03/E-09).
type approvalLedger interface {
	Load(ctx context.Context, requestID string) (approvalRecord, bool, error)
	// Put inserts only when absent; inserted=false returns the stored record.
	Put(ctx context.Context, requestID string, rec approvalRecord) (bool, error)
}

// durableApprovals implements the replay half of SEC-13: the first prompt for
// a fingerprint records the decision and marks it executed, so a re-issued
// prompt after a restart resumes the stored decision without a keyboard and
// without a second execution. Expired records deny and new prompts after the
// TTL are independent requests.
type durableApprovals struct {
	*gateway.Approvals
	ledger   approvalLedger
	now      func() time.Time
	ttl      time.Duration
	newNonce func() string
}

func (d *durableApprovals) decide(ctx context.Context, requestID string, ask func(context.Context, string) (bool, error)) (bool, error) {
	if rec, found, err := d.ledger.Load(ctx, requestID); err != nil {
		return false, fmt.Errorf("approval ledger load: %w", err)
	} else if found {
		now := d.now().UnixMilli()
		switch {
		case now >= rec.ExpiresAt, rec.Decision == "deny", rec.Executed:
			return false, nil
		case rec.Decision == "allow":
			return false, nil // fail-closed: a pending record is never replayed as allow
		}
		return false, nil
	}
	nonce := d.newNonce()
	allow, err := ask(ctx, nonce)
	if err != nil {
		return false, err
	}
	rec := approvalRecord{
		Decision: "deny", NonceSHA: sha12(nonce), ExpiresAt: d.now().Add(d.ttl).UnixMilli(), Executed: true,
	}
	if allow {
		rec.Decision = "allow"
	}
	inserted, err := d.ledger.Put(ctx, requestID, rec)
	if err != nil {
		return false, fmt.Errorf("approval ledger put: %w", err)
	}
	if !inserted {
		// Another process answered first; the store's record wins (fail-closed).
		return false, nil
	}
	return allow, nil
}

func sha12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// approvalRequestID is stable across a crash/resume because it hashes the
// session, the originating user prompt, the tool and the preview.
func approvalRequestID(session, turnKey, tool, preview string) string {
	sum := sha256.Sum256([]byte("approval-v1\x00" + session + "\x00" + turnKey + "\x00" + tool + "\x00" + preview))
	return hex.EncodeToString(sum[:16])
}

func randomNonce() string { return rand.Text()[:16] }

// storeLedger persists approval records in module_state (mutable, no DDL).
type storeLedger struct{ db *ctxdb.Store }

func (s storeLedger) Load(ctx context.Context, requestID string) (approvalRecord, bool, error) {
	var raw string
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT value FROM module_state WHERE module_id = 'approval' AND key = ?1`, []any{requestID}, &raw)
	})
	switch {
	case errors.Is(err, ctxdb.ErrNotFound):
		return approvalRecord{}, false, nil
	case err != nil:
		return approvalRecord{}, false, fmt.Errorf("approval ledger load: %w", err)
	}
	var rec approvalRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return approvalRecord{}, false, fmt.Errorf("approval ledger decode: %w", err)
	}
	return rec, true, nil
}

func (s storeLedger) Put(ctx context.Context, requestID string, rec approvalRecord) (bool, error) {
	b, err := json.Marshal(rec)
	if err != nil {
		return false, fmt.Errorf("approval ledger encode: %w", err)
	}
	var inserted int64
	err = s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		n, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at)
			VALUES ('approval', ?1, ?2, ?3) ON CONFLICT(module_id, key) DO NOTHING`, requestID, string(b), rec.ExpiresAt)
		inserted = n
		if xerr != nil {
			return fmt.Errorf("approval ledger insert: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("approval ledger put: %w", err)
	}
	return inserted == 1, nil
}

// newDurableApprovals assembles the SEC-13 replay ledger over the in-memory
// approval registry used by /approve and /deny.
func newDurableApprovals(ledger approvalLedger) *durableApprovals {
	return &durableApprovals{
		Approvals: gateway.NewApprovals(time.Now),
		ledger:    ledger, now: time.Now, ttl: approvalTTL, newNonce: randomNonce,
	}
}

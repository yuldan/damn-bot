package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/redact"
)

// Store is the persisted side of the memory service (E-09, ≤ 5 methods).
// Insert appends a record and its first decision in one transaction; Decide
// and Pin are scoped (an id outside the caller's scope fails closed); Search
// selects rows of the caller's scopes; Get returns records by id for review.
type Store interface {
	Insert(ctx context.Context, r *Record, d *Decision) (int64, error)
	Decide(ctx context.Context, id int64, scope string, d *Decision) error
	Pin(ctx context.Context, id int64, scope string, pinned bool, actor Actor, at int64) error
	Search(ctx context.Context, q *Query) ([]Hit, error)
	Get(ctx context.Context, ids []int64) ([]Record, error)
}

// Config builds a [Service]. Redactor is required: a memory service that
// cannot redact writes fails closed (mirrors tools.NewDispatcher).
type Config struct {
	Now      func() int64
	Redactor *redact.Redactor
}

// Service is the memory API. It is safe for concurrent use when the Store is.
type Service struct {
	store Store
	red   *redact.Redactor
	now   func() int64
}

// New validates the dependencies.
func New(st Store, cfg Config) (*Service, error) {
	if st == nil {
		return nil, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if cfg.Redactor == nil {
		return nil, fmt.Errorf("%w: nil redactor", ErrInvalid)
	}
	now := cfg.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Service{store: st, red: cfg.Redactor, now: now}, nil
}

// Propose writes one entry. User-origin writes are redacted, scanned and
// stored approved (SPEC §4.8: manual user writes need no review). Every other
// origin is quarantined: stored as scanned for review, or rejected
// immediately when the threat scan is high (P10, §14.4). The returned record
// carries the resulting status and scan result.
func (s *Service) Propose(ctx context.Context, p *Proposal) (Record, error) {
	if err := p.validate(); err != nil {
		return Record{}, err
	}
	scope, err := normalizeScope(p.Scope)
	if err != nil {
		return Record{}, err
	}
	scan := ScanText(s.red, p.Content)
	content := s.red.String(p.Content)
	if utf8.RuneCountInString(content) > MaxContentChars {
		return Record{}, fmt.Errorf("%w: redacted content > %d chars", ErrTooLarge, MaxContentChars)
	}
	scanJSON, err := scan.JSON()
	if err != nil {
		return Record{}, fmt.Errorf("memory: scan result: %w", err)
	}
	if p.SupersedesID != nil {
		old, gerr := s.store.Get(ctx, []int64{*p.SupersedesID})
		if gerr != nil {
			return Record{}, fmt.Errorf("memory: supersedes: %w", gerr)
		}
		if len(old) != 1 || old[0].Scope != scope {
			return Record{}, fmt.Errorf("%w: supersedes entry %d is not in scope %q", ErrScope, *p.SupersedesID, scope)
		}
	}
	now := s.now()
	rec := Record{
		Category: p.Category, Scope: scope, Content: content, Origin: p.Origin,
		SourceSession: p.SourceSession, EvidenceEventID: p.EvidenceEventID,
		SupersedesID: p.SupersedesID, ExpiresAt: p.ExpiresAt, ScanResult: scanJSON,
		CreatedAt: now, Status: StatusScanned,
	}
	d := Decision{Status: StatusScanned, Actor: ActorSystem, Reason: "scan:" + string(scan.Severity), CreatedAt: now}
	if p.Origin.Trusted() {
		rec.Status, d.Status, d.Actor, d.Reason = StatusApproved, StatusApproved, ActorUser, "user"
	} else if scan.High() {
		rec.Status, d.Status, d.Reason = StatusRejected, StatusRejected, "scan:high"
	}
	id, err := s.store.Insert(ctx, &rec, &d)
	if err != nil {
		return Record{}, s.classify(err)
	}
	rec.ID = id
	return rec, nil
}

// Approve records a user approval (scanned → approved) of an entry in scope.
func (s *Service) Approve(ctx context.Context, id int64, scope string) error {
	return s.decide(ctx, id, scope, StatusApproved, "confirmed")
}

// Reject discards a quarantined entry in scope (scanned → rejected).
func (s *Service) Reject(ctx context.Context, id int64, scope string) error {
	return s.decide(ctx, id, scope, StatusRejected, "rejected")
}

// Retire is the logical delete of an approved entry in scope (approved →
// retired); the row itself is immutable and only a purge removes it (P9).
func (s *Service) Retire(ctx context.Context, id int64, scope string) error {
	return s.decide(ctx, id, scope, StatusRetired, "retired")
}

// Confirm re-approves an expired entry in scope (expired → approved).
func (s *Service) Confirm(ctx context.Context, id int64, scope string) error {
	return s.decide(ctx, id, scope, StatusApproved, "confirmed")
}

// Pin sets or clears the pin flag of an entry in scope (append-only
// memory_pins).
func (s *Service) Pin(ctx context.Context, id int64, scope string, pinned bool) error {
	normalized, serr := normalizeScope(scope)
	if id <= 0 || serr != nil {
		return fmt.Errorf("%w: id/scope", ErrInvalid)
	}
	if err := s.store.Pin(ctx, id, normalized, pinned, ActorUser, s.now()); err != nil {
		return fmt.Errorf("memory: pin: %w", err)
	}
	return nil
}

// Recall is the retrieval API: approved, unexpired entries of the active
// scopes, pinned first and then BM25 against text. Only entries the assembler
// may show are returned.
func (s *Service) Recall(ctx context.Context, r *Recall) ([]Hit, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: nil recall", ErrInvalid)
	}
	limit := r.Limit
	if limit <= 0 || limit > MaxRecall {
		limit = DefaultRecall
	}
	scopes, err := normalizeScopes(r.Scopes)
	if err != nil {
		return nil, err
	}
	hits, err := s.store.Search(ctx, &Query{
		Text: r.Text, Scopes: scopes, Statuses: []Status{StatusApproved},
		Limit: limit, Now: s.now(),
	})
	if err != nil {
		return nil, fmt.Errorf("memory: recall: %w", err)
	}
	return hits, nil
}

// Pending lists quarantined entries of exactly the caller's scopes for review
// (`damnbot review`, TUI). Empty scopes deny (JN3-1).
func (s *Service) Pending(ctx context.Context, scopes []string, limit int) ([]Hit, error) {
	return s.review(ctx, scopes, StatusScanned, limit)
}

// Rejected lists rejected entries of exactly the caller's scopes (`damnbot
// review --rejected`): the audit view of what quarantine discarded.
func (s *Service) Rejected(ctx context.Context, scopes []string, limit int) ([]Hit, error) {
	return s.review(ctx, scopes, StatusRejected, limit)
}

func (s *Service) review(ctx context.Context, scopes []string, st Status, limit int) ([]Hit, error) {
	normalized, err := normalizeScopes(scopes)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxRecall {
		limit = MaxRecall
	}
	hits, err := s.store.Search(ctx, &Query{Scopes: normalized, Statuses: []Status{st}, Limit: limit, Now: s.now()})
	if err != nil {
		return nil, fmt.Errorf("memory: review %s: %w", st, err)
	}
	return hits, nil
}

// Get returns records by id regardless of status (review, tests).
func (s *Service) Get(ctx context.Context, ids []int64) ([]Record, error) {
	recs, err := s.store.Get(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("memory: get: %w", err)
	}
	return recs, nil
}

func (s *Service) decide(ctx context.Context, id int64, scope string, st Status, reason string) error {
	normalized, serr := normalizeScope(scope)
	if id <= 0 || serr != nil {
		return fmt.Errorf("%w: id/scope", ErrInvalid)
	}
	d := &Decision{Status: st, Actor: ActorUser, Reason: reason, CreatedAt: s.now()}
	return s.classify(s.store.Decide(ctx, id, normalized, d))
}

// normalizeScopes rejects an empty set (empty must never mean "all scopes")
// and returns portable scope forms.
func normalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, fmt.Errorf("%w: at least one scope is required", ErrInvalid)
	}
	out := make([]string, len(scopes))
	for i, scope := range scopes {
		normalized, err := normalizeScope(scope)
		if err != nil {
			return nil, err
		}
		out[i] = normalized
	}
	return out, nil
}

// classify maps a storage cap error to the consumer's ErrTooLarge so callers
// match one error regardless of the Store implementation.
func (s *Service) classify(err error) error {
	if err == nil {
		return nil
	}
	var le LimitError
	if errors.As(err, &le) && le.TooLarge() {
		return fmt.Errorf("%w: %w", ErrTooLarge, err)
	}
	return err
}

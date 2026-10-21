package memory

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits of SPEC §4.8/P10. The per-scope cap counts characters of approved
// entries, exactly like the SQLite length() the storage uses.
const (
	MaxContentChars = 500
	MaxScopeChars   = 4000
	DefaultRecall   = 10
	MaxRecall       = 50
)

// Category is the kind of a memory entry (memory.category, SPEC §4.8).
type Category string

// Memory categories (SPEC §4.8).
const (
	CategoryFact       Category = "fact"
	CategoryPreference Category = "preference"
	CategoryProject    Category = "project"
	CategoryProcedure  Category = "procedure"
)

// Valid reports whether c is one of the four stored categories.
func (c Category) Valid() bool {
	switch c {
	case CategoryFact, CategoryPreference, CategoryProject, CategoryProcedure:
		return true
	}
	return false
}

// Origin is who produced an entry (memory.origin).
type Origin string

// Memory origins (SPEC §11.2).
const (
	OriginAgent  Origin = "agent"
	OriginUser   Origin = "user"
	OriginAuto   Origin = "auto"
	OriginImport Origin = "import"
	OriginHub    Origin = "hub"
)

// Valid reports whether o is a stored origin.
func (o Origin) Valid() bool {
	switch o {
	case OriginAgent, OriginUser, OriginAuto, OriginImport, OriginHub:
		return true
	}
	return false
}

// NeedsEvidence reports whether the origin must cite an event (P10: agent,
// auto and imported content is quarantined and must be traceable).
func (o Origin) NeedsEvidence() bool { return o == OriginAgent || o == OriginAuto }

// Trusted reports whether an origin may write without quarantine. Only an
// explicit user action is trusted; every other origin is scanned first.
func (o Origin) Trusted() bool { return o == OriginUser }

// Status is the latest decision in memory_decisions (SPEC §4.8 diagram).
type Status string

// Memory lifecycle states.
const (
	StatusPending  Status = "pending"
	StatusScanned  Status = "scanned"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusRetired  Status = "retired"
	StatusExpired  Status = "expired"
)

// Valid reports whether s is a stored decision status.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusScanned, StatusApproved, StatusRejected, StatusRetired, StatusExpired:
		return true
	}
	return false
}

// Actor is who made a decision (memory_decisions.actor).
type Actor string

// Decision actors.
const (
	ActorSystem Actor = "system"
	ActorUser   Actor = "user"
)

// Valid reports whether a is a stored actor.
func (a Actor) Valid() bool { return a == ActorSystem || a == ActorUser }

// Record is one memory entry plus its effective status (v_memory_status).
// The fields mirror memory columns one for one; memstore.Row has the same
// fields so the composition root converts without either package importing
// the other (ADR-074, like ledgerstore).
type Record struct {
	ID              int64
	Category        Category
	Scope           string
	Content         string
	Origin          Origin
	SourceSession   string
	EvidenceEventID *int64
	SupersedesID    *int64
	ExpiresAt       *int64
	ScanResult      string
	CreatedAt       int64
	Status          Status
	Pinned          bool
	LastConfirmedAt *int64
}

// Decision is one append-only lifecycle row.
type Decision struct {
	Status    Status
	Actor     Actor
	Reason    string
	CreatedAt int64
}

// Query selects retrievable records. Statuses defaults to approved and the
// storage additionally drops expired entries and entries whose provenance
// session is being purged (SPEC §11.6).
type Query struct {
	Text     string
	Scopes   []string
	Statuses []Status
	Limit    int
	Now      int64
}

// Hit is one retrieval result; the slice order is the contract (pinned
// first, then BM25 rank). No score is exposed: every storage path already
// orders by bm25() and a consumer must not re-rank.
type Hit struct {
	Record Record
}

// Proposal is one write request. Trusted origins are stored approved after
// redaction; everything else is quarantined under the scan result.
type Proposal struct {
	Category        Category
	Scope           string
	Content         string
	Origin          Origin
	SourceSession   string
	EvidenceEventID *int64
	SupersedesID    *int64
	ExpiresAt       *int64
}

// Recall is one retrieval request. Scopes empty means every active scope.
type Recall struct {
	Text   string
	Scopes []string
	Limit  int
}

func (p *Proposal) validate() error {
	if !p.Category.Valid() {
		return fmt.Errorf("%w: category %q", ErrInvalid, p.Category)
	}
	if !p.Origin.Valid() {
		return fmt.Errorf("%w: origin %q", ErrInvalid, p.Origin)
	}
	if err := validateScope(p.Scope); err != nil {
		return err
	}
	if strings.TrimSpace(p.Content) == "" {
		return fmt.Errorf("%w: empty content", ErrInvalid)
	}
	if utf8.RuneCountInString(p.Content) > MaxContentChars {
		return fmt.Errorf("%w: content > %d chars", ErrTooLarge, MaxContentChars)
	}
	if p.Origin.NeedsEvidence() && p.EvidenceEventID == nil {
		return fmt.Errorf("%w: origin %q requires evidence_event_id", ErrInvalid, p.Origin)
	}
	if p.EvidenceEventID != nil && *p.EvidenceEventID <= 0 {
		return fmt.Errorf("%w: evidence_event_id must be positive", ErrInvalid)
	}
	if p.SupersedesID != nil && *p.SupersedesID <= 0 {
		return fmt.Errorf("%w: supersedes_id must be positive", ErrInvalid)
	}
	return nil
}

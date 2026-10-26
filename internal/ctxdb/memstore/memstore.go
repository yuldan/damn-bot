// Package memstore is the ctxdb storage of internal/memory (SPEC §4.8,
// §11.2, §11.6; ADR-074, ADR-120). The tables, FTS index, append-only
// triggers and the purge exception already exist in migration 0001, so this
// package adds no DDL: it only enforces the storage invariants the consumer
// relies on (latest-decision state machine, per-scope cap, provenance purge
// filters) inside one writer transaction.
//
// Row, Decision, Query and Hit mirror memory.Record, memory.Decision,
// memory.Query and memory.Hit field for field. Neither package imports the
// other; cmd/damnbot converts and supplies the adapter.
package memstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
)

// Limits of SPEC §4.8/P10; mirrored in internal/memory.
const (
	MaxContentChars = 500
	MaxScopeChars   = 4000
)

// Closed storage errors. The consumer maps TooLarge through its own
// LimitError interface (ADR-120 butir 6).
var (
	ErrInvalid    = errors.New("memstore: invalid row")
	ErrNotFound   = errors.New("memstore: not found")
	ErrTransition = errors.New("memstore: invalid transition")
	// ErrScope is a cross-scope id or supersede target (JN3-2).
	ErrScope = errors.New("memstore: scope mismatch")
	// ErrPolicy is an initial approved decision for an untrusted origin
	// (JN3-8): only user writes may be stored approved.
	ErrPolicy = errors.New("memstore: quarantine required")
	// ErrNotAgent rejects a system-actor retirement of a non-agent row
	// (SEC17-3): the tool path retires only what the agent wrote.
	ErrNotAgent = errors.New("memstore: not an agent-origin entry")
)

// TooLarge reports a P10 per-scope cap violation through TooLarge(), the
// structural interface memory.LimitError expects.
type TooLarge struct{}

// Error implements error.
func (TooLarge) Error() string { return "memstore: approved scope cap exceeded" }

// TooLarge makes TooLarge satisfy memory.LimitError.
func (TooLarge) TooLarge() bool { return true }

// Row is one memory row plus effective status (v_memory_status).
type Row struct {
	ID              int64
	Category        string
	Scope           string
	Content         string
	Origin          string
	SourceSession   string
	EvidenceEventID *int64
	SupersedesID    *int64
	ExpiresAt       *int64
	ScanResult      string
	CreatedAt       int64
	Status          string
	Pinned          bool
	LastConfirmedAt *int64
}

// Decision is one memory_decisions row.
type Decision struct {
	Status    string
	Actor     string
	Reason    string
	CreatedAt int64
}

// Query selects rows. Statuses defaults to approved.
type Query struct {
	Text     string
	Scopes   []string
	Statuses []string
	Limit    int
	Now      int64
}

// Hit is one ordered result.
type Hit struct {
	Row Row
}

// Insert appends r and its first decision inside tx. Content is redacted
// again at the storage boundary (defense in depth, ADR-120 butir 5); an
// approved row is checked against the per-scope cap and retires its
// supersedes target in the same transaction.
func Insert(tx *ctxdb.Tx, r *Row, d *Decision, red *redact.Redactor) (int64, error) {
	row, err := prepare(tx, r, d, red)
	if err != nil {
		return 0, err
	}
	encID, ierr := newEncID()
	if ierr != nil {
		return 0, ierr
	}
	contentEnc, contentStored, serr := tx.SealRow("memory.content", row.Scope+"|"+encID, row.Content, "")
	if serr != nil {
		return 0, fmt.Errorf("memstore seal: %w", serr)
	}
	var id int64
	err = tx.Get(`INSERT INTO memory(kind, category, scope, name, content, content_enc, content_len, enc_id, origin, source_session,
		evidence_event_id, supersedes_id, expires_at, scan_result, created_at)
		VALUES ('memory', ?1, ?2, NULL, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13) RETURNING id`,
		[]any{
			row.Category, row.Scope, contentStored, nullBytes(contentEnc), utf8.RuneCountInString(row.Content), encID, row.Origin, nullable(row.SourceSession),
			row.EvidenceEventID, row.SupersedesID, row.ExpiresAt, row.ScanResult, row.CreatedAt,
		}, &id)
	if err != nil {
		return 0, fmt.Errorf("memstore insert: %w", err)
	}
	if err := insertDecision(tx, id, d); err != nil {
		return 0, err
	}
	if d.Status == "approved" && row.SupersedesID != nil {
		if err := retireSuperseded(tx, id, *row.SupersedesID, row.Scope, d.CreatedAt); err != nil {
			return 0, err
		}
	}
	r.ID, r.Content = id, row.Content
	return id, nil
}

func prepare(tx *ctxdb.Tx, r *Row, d *Decision, red *redact.Redactor) (*Row, error) {
	if red == nil {
		return nil, fmt.Errorf("%w: nil redactor", ErrInvalid)
	}
	row := *r
	row.Content = red.String(row.Content)
	if err := validate(&row, d); err != nil {
		return nil, err
	}
	if d.Status == "approved" && row.Origin != "user" {
		return nil, fmt.Errorf("%w: origin %q", ErrPolicy, row.Origin)
	}
	if row.SupersedesID != nil {
		if err := sameScope(tx, *row.SupersedesID, row.Scope); err != nil {
			return nil, err
		}
	}
	if d.Status == "approved" {
		if err := checkCap(tx, row.Scope, row.Content, 0); err != nil {
			return nil, err
		}
	}
	return &row, nil
}

// Put is Insert in its own transaction.
func Put(ctx context.Context, s *ctxdb.Store, r *Row, d *Decision, red *redact.Redactor) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	var id int64
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		var ierr error
		id, ierr = Insert(tx, r, d, red)
		return ierr
	})
	if err != nil {
		return 0, fmt.Errorf("memstore put: %w", err)
	}
	return id, nil
}

// Decide appends one lifecycle decision. The state machine and the cap are
// checked in the same transaction; approving a superseding row retires the
// old approved row.
func Decide(ctx context.Context, s *ctxdb.Store, id int64, scope string, d *Decision) error {
	if s == nil {
		return fmt.Errorf("%w: nil store", ErrInvalid)
	}
	return wrap(s.Write(ctx, func(tx *ctxdb.Tx) error { return decide(tx, id, scope, d) }))
}

// rowState is the current shape of a memory row: decide needs the status it
// transitions from, the origin SEC17-3 gates on and the row it supersedes.
type rowState struct {
	scope      string
	content    string
	status     string
	origin     string
	supersedes sql.NullInt64
}

// loadRow reads the row decide is about to transition.
func loadRow(tx *ctxdb.Tx, id int64) (rowState, error) {
	var r rowState
	err := tx.Get(`SELECT scope, content, status, supersedes_id, origin FROM v_memory_status WHERE id = ?1`, []any{id},
		&r.scope, &r.content, &r.status, &r.supersedes, &r.origin)
	if errors.Is(err, ctxdb.ErrNotFound) {
		return rowState{}, fmt.Errorf("%w: %d", ErrNotFound, id)
	}
	if err != nil {
		return rowState{}, fmt.Errorf("memstore decide: %w", err)
	}
	return r, nil
}

func decide(tx *ctxdb.Tx, id int64, scope string, d *Decision) error {
	if id <= 0 || !validStatus(d.Status) || !validActor(d.Actor) {
		return ErrInvalid
	}
	row, err := loadRow(tx, id)
	if err != nil {
		return err
	}
	if row.scope != scope {
		return fmt.Errorf("%w: %d is in %q", ErrScope, id, row.scope)
	}
	// SEC17-3: the agent tool path decides as "system" and may retire only an
	// agent-origin row; the user path decides as "user".
	if d.Actor == "system" && d.Status == "retired" && row.origin != "agent" {
		return fmt.Errorf("%w: %d is origin %q", ErrNotAgent, id, row.origin)
	}
	if !transition(row.status, d.Status) {
		return fmt.Errorf("%w: %s -> %s", ErrTransition, row.status, d.Status)
	}
	if d.Status == "approved" {
		if err := checkCap(tx, scope, row.content, id); err != nil {
			return err
		}
	}
	if err := insertDecision(tx, id, d); err != nil {
		return err
	}
	if d.Status == "approved" && row.supersedes.Valid {
		return retireSuperseded(tx, id, row.supersedes.Int64, row.scope, d.CreatedAt)
	}
	return nil
}

// Pin appends one memory_pins row.
func Pin(ctx context.Context, s *ctxdb.Store, id int64, scope string, pinned bool, actor string, at int64) error {
	if s == nil {
		return fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if id <= 0 || !validActor(actor) {
		return ErrInvalid
	}
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		if serr := sameScope(tx, id, scope); serr != nil {
			return serr
		}
		_, xerr := tx.Exec(`INSERT INTO memory_pins(memory_id, pinned, actor, created_at) VALUES (?1, ?2, ?3, ?4)`,
			id, pinned, actor, at)
		if xerr != nil {
			return fmt.Errorf("memstore pin: %w", xerr)
		}
		return nil
	})
	return wrap(err)
}

// Search returns the ordered ids of matching rows: pinned first, then bm25
// for a text query (newest first without one). It filters the latest status,
// expiry and the purge labels of both provenance paths (§11.6).
func Search(ctx context.Context, s *ctxdb.Store, q *Query) ([]Hit, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	var hits []Hit
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, ierr := searchIDs(tx, q)
		if ierr != nil {
			return ierr
		}
		hits = make([]Hit, 0, len(ids))
		for _, id := range ids {
			r, rerr := row(tx, id)
			if rerr != nil {
				return rerr
			}
			hits = append(hits, Hit{Row: r})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("memstore search: %w", err)
	}
	return hits, nil
}

func searchIDs(tx *ctxdb.Tx, q *Query) ([]int64, error) {
	if q == nil {
		q = &Query{}
	}
	if len(q.Scopes) == 0 {
		return nil, nil // empty scope set denies (JN3-1)
	}
	statuses := q.Statuses
	if len(statuses) == 0 {
		statuses = []string{"approved"}
	}
	args := make([]any, 0, 4+len(statuses)+len(q.Scopes))
	where := make([]string, 0, 6)
	where = append(where, "m.kind = 'memory'", "m.status IN ("+marks(len(statuses))+")")
	for _, st := range statuses {
		args = append(args, st)
	}
	from := "v_memory_status m"
	text := ftsQuery(q.Text)
	if text != "" {
		text = tx.FTSQuery(text)
	}
	if text == "" && strings.TrimSpace(q.Text) != "" {
		return nil, nil
	}
	if text != "" {
		from = "memory_fts f JOIN v_memory_status m ON m.id = f.rowid"
		where = append(where, "memory_fts MATCH ?")
		args = append(args, text)
	}
	if len(q.Scopes) > 0 {
		where = append(where, "m.scope IN ("+marks(len(q.Scopes))+")")
		for _, sc := range q.Scopes {
			args = append(args, sc)
		}
	}
	where = append(where, "(m.expires_at IS NULL OR m.expires_at > ?)")
	args = append(args, q.Now)
	where = append(where,
		"NOT EXISTS (SELECT 1 FROM session_labels vl WHERE vl.session_id = m.source_session AND vl.key = 'purge_state')",
		"NOT EXISTS (SELECT 1 FROM events ev JOIN session_labels vle ON vle.session_id = ev.session_id AND vle.key = 'purge_state' WHERE ev.id = m.evidence_event_id)")
	order := "m.pinned DESC, m.created_at DESC, m.id"
	if text != "" {
		order = "m.pinned DESC, bm25(memory_fts), m.id" // equal scores keep insertion order
	}
	limit := q.Limit
	if limit <= 0 {
		limit = -1
	}
	args = append(args, limit)
	query := "SELECT m.id FROM " + from + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + order + " LIMIT ?"
	ids, err := tx.Ints(query, args...)
	if err != nil {
		return nil, fmt.Errorf("memstore ids: %w", err)
	}
	return ids, nil
}

// Get returns the rows of ids that exist, in the requested order.
func Get(ctx context.Context, s *ctxdb.Store, ids []int64) ([]Row, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	out := make([]Row, 0, len(ids))
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		for _, id := range ids {
			r, rerr := row(tx, id)
			if errors.Is(rerr, sql.ErrNoRows) || errors.Is(rerr, ctxdb.ErrNotFound) {
				continue
			}
			if rerr != nil {
				return rerr
			}
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("memstore get: %w", err)
	}
	return out, nil
}

// Expire appends an expired decision to every approved row whose expires_at
// has passed; retrieval already hides them, this only makes the ledger state
// explicit (SPEC §4.8: expired entries are proposed for reconfirmation).
func Expire(ctx context.Context, s *ctxdb.Store, now int64) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	var n int64
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, ierr := tx.Ints(`SELECT id FROM v_memory_status
			WHERE status = 'approved' AND expires_at IS NOT NULL AND expires_at <= ?1`, now)
		if ierr != nil {
			return fmt.Errorf("memstore expire ids: %w", ierr)
		}
		for _, id := range ids {
			d := &Decision{Status: "expired", Actor: "system", Reason: "expired", CreatedAt: now}
			if err := insertDecision(tx, id, d); err != nil {
				return err
			}
		}
		n = int64(len(ids))
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("memstore expire: %w", err)
	}
	return n, nil
}

func row(tx *ctxdb.Tx, id int64) (Row, error) {
	var r Row
	var ev, sup, exp, last sql.NullInt64
	var contentEnc []byte
	var encID string
	err := tx.Get(`SELECT id, category, scope, content, content_enc, enc_id, origin, COALESCE(source_session, ''),
		evidence_event_id, supersedes_id, expires_at, scan_result, created_at, status, pinned, last_confirmed_at
		FROM v_memory_status WHERE id = ?1`, []any{id},
		&r.ID, &r.Category, &r.Scope, &r.Content, &contentEnc, &encID, &r.Origin, &r.SourceSession,
		&ev, &sup, &exp, &r.ScanResult, &r.CreatedAt, &r.Status, &r.Pinned, &last)
	if err != nil {
		return Row{}, err //nolint:wrapcheck // classify at the exported boundary (Get/Search); callers here add context
	}
	if r.Content, err = tx.OpenRow("memory.content", r.Scope+"|"+encID, contentEnc, r.Content); err != nil {
		return Row{}, fmt.Errorf("memstore decrypt: %w", err)
	}
	r.EvidenceEventID, r.SupersedesID, r.ExpiresAt = ptr(ev), ptr(sup), ptr(exp)
	r.LastConfirmedAt = ptr(last)
	return r, nil
}

func insertDecision(tx *ctxdb.Tx, id int64, d *Decision) error {
	_, err := tx.Exec(`INSERT INTO memory_decisions(memory_id, status, actor, reason, created_at)
		VALUES (?1, ?2, ?3, ?4, ?5)`, id, d.Status, d.Actor, d.Reason, d.CreatedAt)
	if err != nil {
		return fmt.Errorf("memstore decision: %w", err)
	}
	return nil
}

// checkCap sums the characters of approved entries in scope plus content,
// excluding self (the row being approved); over MaxScopeChars is TooLarge.
func checkCap(tx *ctxdb.Tx, scope, content string, self int64) error {
	var total int64
	err := tx.Get(`SELECT COALESCE(SUM(COALESCE(m.content_len, length(m.content))), 0) + length(?3)
		FROM memory m
		JOIN memory_decisions d ON d.id = (SELECT id FROM memory_decisions WHERE memory_id = m.id ORDER BY id DESC LIMIT 1)
		WHERE m.kind = 'memory' AND m.scope = ?1 AND d.status = 'approved' AND m.id <> ?2`,
		[]any{scope, self, content}, &total)
	if err != nil {
		return fmt.Errorf("memstore cap: %w", err)
	}
	if total > MaxScopeChars {
		return TooLarge{}
	}
	return nil
}

// retireSuperseded retires oldID when it is still approved; the new row's
// approval is the only trigger (SPEC §4.8).
func retireSuperseded(tx *ctxdb.Tx, newID, oldID int64, scope string, at int64) error {
	var gotScope, status string
	err := tx.Get(`SELECT scope, status FROM v_memory_status WHERE id = ?1`, []any{oldID}, &gotScope, &status)
	if errors.Is(err, ctxdb.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memstore supersedes: %w", err)
	}
	if gotScope != scope {
		return fmt.Errorf("%w: supersedes %d is in %q", ErrScope, oldID, gotScope)
	}
	if status != "approved" {
		return nil
	}
	d := &Decision{Status: "retired", Actor: "system", Reason: fmt.Sprintf("superseded:%d", newID), CreatedAt: at}
	return insertDecision(tx, oldID, d)
}

// sameScope checks that id is a memory row in scope; it is the ownership
// check of Decide/Pin/supersede (JN3-2).
func sameScope(tx *ctxdb.Tx, id int64, scope string) error {
	if scope == "" {
		return fmt.Errorf("%w: empty scope", ErrScope)
	}
	var got string
	err := tx.Get(`SELECT scope FROM memory WHERE id = ?1 AND kind = 'memory'`, []any{id}, &got)
	if errors.Is(err, ctxdb.ErrNotFound) {
		return fmt.Errorf("%w: %d", ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("memstore scope: %w", err)
	}
	if got != scope {
		return fmt.Errorf("%w: %d is in %q", ErrScope, id, got)
	}
	return nil
}

func validate(r *Row, d *Decision) error {
	switch r.Category {
	case "fact", "preference", "project", "procedure":
	default:
		return fmt.Errorf("%w: category %q", ErrInvalid, r.Category)
	}
	switch r.Origin {
	case "agent", "user", "auto", "import", "hub":
	default:
		return fmt.Errorf("%w: origin %q", ErrInvalid, r.Origin)
	}
	if !validScope(r.Scope) {
		return fmt.Errorf("%w: scope %q", ErrInvalid, r.Scope)
	}
	if strings.TrimSpace(r.Content) == "" {
		return fmt.Errorf("%w: empty content", ErrInvalid)
	}
	if utf8.RuneCountInString(r.Content) > MaxContentChars {
		return fmt.Errorf("%w: content > %d chars", ErrInvalid, MaxContentChars)
	}
	if (r.Origin == "agent" || r.Origin == "auto") && r.EvidenceEventID == nil {
		return fmt.Errorf("%w: origin %q requires evidence_event_id", ErrInvalid, r.Origin)
	}
	if !validStatus(d.Status) || !validActor(d.Actor) {
		return fmt.Errorf("%w: decision %+v", ErrInvalid, *d)
	}
	return nil
}

func validScope(scope string) bool {
	if scope == "user" {
		return true
	}
	if rest, ok := strings.CutPrefix(scope, "project:"); ok {
		return path.IsAbs(rest) || filepath.IsAbs(filepath.FromSlash(rest))
	}
	rest, ok := strings.CutPrefix(scope, "gateway:")
	if !ok {
		return false
	}
	platform, identity, found := strings.Cut(rest, ":")
	return found && platform != "" && identity != ""
}

func validStatus(s string) bool {
	switch s {
	case "pending", "scanned", "approved", "rejected", "retired", "expired":
		return true
	}
	return false
}

func validActor(a string) bool { return a == "user" || a == "system" }

// transition is the closed state machine of SPEC §4.8; re-approving an
// approved row is a confirmation (a new row updates last_confirmed_at).
func transition(from, to string) bool {
	switch from {
	case "pending":
		return to == "scanned" || to == "approved" || to == "rejected"
	case "scanned":
		return to == "approved" || to == "rejected"
	case "approved":
		return to == "approved" || to == "retired" || to == "expired"
	case "expired":
		return to == "approved"
	case "rejected", "retired":
		return false
	}
	return false
}

// ftsQuery turns free text into a conservative FTS5 expression: every run of
// letters/digits is a term and everything else becomes a separator, so user
// text can never produce a MATCH syntax error (quotes, '-', parentheses,
// column filters). Empty result means "no text query".
// maxFTSTerms bounds a recall query; longer user messages would otherwise
// build an unbounded MATCH expression.
const maxFTSTerms = 64

func ftsQuery(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range ctxdb.FTSNorm(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	fields := strings.Fields(b.String())
	seen := make(map[string]struct{}, min(len(fields), maxFTSTerms))
	tokens := make([]string, 0, len(seen))
	for _, tok := range fields {
		if len(tokens) == maxFTSTerms {
			break
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		// Quoted, so a user word that happens to spell an operator ("or",
		// "and") stays a term instead of changing the expression.
		tokens = append(tokens, `"`+tok+`"`)
	}
	// BM25 ranking (SPEC §4.8/B-03) scores documents matching any query
	// term: a natural-language question ("What is my locker number?") does
	// not contain every word of the stored fact, so an AND match would
	// recall nothing. Rank order still decides; LIMIT bounds the candidates.
	return strings.Join(tokens, " OR ")
}

func marks(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func newEncID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("memstore enc id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("memstore: %w", err)
}

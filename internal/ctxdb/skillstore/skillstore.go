// Package skillstore is the ctxdb storage of internal/skills (SPEC §4.8,
// §11.2; ADR-074, ADR-120). The skills table and its FTS index already exist
// in migration 0001, and a proposed skill's quarantine state lives in the
// memory tables (kind = 'skill'), linked by skills.memory_id. This package
// adds no DDL; it enforces idempotent import, the approval link, and the
// "approved only" retrieval filter.
//
// Row mirrors skills.Skill field for field; neither package imports the
// other, and cmd/damnbot supplies the adapter.
package skillstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
)

// MaxBodyBytes is the skill content cap of SPEC §4.8.
const MaxBodyBytes = 8192

// MaxRequiresBytes bounds the stored host requirement JSON (GAP-TOOL-10):
// comfortable for bins/os/arch/gpu lists, small enough to keep the row cheap.
const MaxRequiresBytes = 2048

// Closed storage errors.
var (
	ErrInvalid    = errors.New("skillstore: invalid row")
	ErrNotFound   = errors.New("skillstore: not found")
	ErrTransition = errors.New("skillstore: invalid transition")
	// ErrPolicy rejects a trusted Index for an untrusted source (JN3-8).
	ErrPolicy = errors.New("skillstore: quarantine required")
)

// Row is one skills row plus its quarantine link (0 = trusted, NULL).
type Row struct {
	ID          int64
	Name        string
	Description string
	Body        string
	Path        string
	SHA256      string
	Source      string
	MemoryID    int64
	// RequiresHost is the JSON host requirement stored with the row
	// (GAP-TOOL-10, ADR-208); "" means the skill declares none.
	RequiresHost string
}

// Index upserts a trusted skill. Re-indexing the same name with a different
// file removes the stale row first (skills is mutable), so one name has at
// most one searchable row; the same (name, sha256) is idempotent.
func Index(ctx context.Context, s *ctxdb.Store, r *Row, at int64) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	switch r.Source {
	case "user", "builtin":
	case "hub", "agent", "import":
		return 0, fmt.Errorf("%w: source %q", ErrPolicy, r.Source)
	default:
		return 0, fmt.Errorf("%w: source %q", ErrInvalid, r.Source)
	}
	var id int64
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		var ierr error
		id, ierr = indexTx(tx, redacted(r), at, true)
		return ierr
	})
	if err != nil {
		return 0, fmt.Errorf("skillstore index: %w", err)
	}
	return id, nil
}

// Quarantine stores a proposed skill as memory(kind='skill') with its scan
// decision and indexes the row linked to it. The index row exists but no
// retrieval path returns it before approval (P10).
func Quarantine(ctx context.Context, s *ctxdb.Store, r *Row, scan string, rejected bool, at int64) (int64, int64, error) {
	if s == nil {
		return 0, 0, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if r.Source != "agent" && r.Source != "hub" && r.Source != "import" {
		return 0, 0, fmt.Errorf("%w: untrusted source %q", ErrInvalid, r.Source)
	}
	var memoryID, skillID int64
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		var existing, existingMem int64
		gerr := tx.Get(`SELECT id, COALESCE(memory_id, 0) FROM skills WHERE name = ?1 AND sha256 = ?2`,
			[]any{r.Name, r.SHA256}, &existing, &existingMem)
		switch {
		case gerr == nil && existingMem == 0:
			memoryID, skillID = 0, existing // trusted row wins, never shadow it
			return nil
		case gerr == nil:
			memoryID, skillID = existingMem, existing // idempotent reinstall
			return nil
		case !errors.Is(gerr, ctxdb.ErrNotFound):
			return fmt.Errorf("skillstore existing: %w", gerr)
		}
		stored := redacted(r)
		mid, merr := quarantineMemory(tx, stored, scan, rejected, at)
		if merr != nil {
			return merr
		}
		memoryID = mid
		linked := *stored
		linked.MemoryID = mid
		var ierr error
		skillID, ierr = indexTx(tx, &linked, at, false)
		return ierr
	})
	if err != nil {
		return 0, 0, fmt.Errorf("skillstore quarantine: %w", err)
	}
	return memoryID, skillID, nil
}

// Approve records the user approval of a quarantined skill (scanned →
// approved); the skills row is already indexed and starts matching.
func Approve(ctx context.Context, s *ctxdb.Store, memoryID int64, at int64) error {
	if s == nil {
		return fmt.Errorf("%w: nil store", ErrInvalid)
	}
	if memoryID <= 0 {
		return fmt.Errorf("%w: memory id", ErrInvalid)
	}
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		var status string
		gerr := tx.Get(`SELECT status FROM v_memory_status WHERE id = ?1 AND kind = 'skill' AND scope = 'user'`, []any{memoryID}, &status)
		if errors.Is(gerr, ctxdb.ErrNotFound) {
			return fmt.Errorf("%w: memory %d", ErrNotFound, memoryID)
		}
		if gerr != nil {
			return fmt.Errorf("skillstore approve: %w", gerr)
		}
		if status != "scanned" {
			return fmt.Errorf("%w: %s -> approved", ErrTransition, status)
		}
		_, xerr := tx.Exec(`INSERT INTO memory_decisions(memory_id, status, actor, reason, created_at)
			VALUES (?1, 'approved', 'user', 'confirmed', ?2)`, memoryID, at)
		if xerr != nil {
			return fmt.Errorf("skillstore decision: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("skillstore approve: %w", err)
	}
	return nil
}

// Status reads the quarantine state of a proposed skill.
func Status(ctx context.Context, s *ctxdb.Store, memoryID int64) (string, error) {
	if s == nil {
		return "", fmt.Errorf("%w: nil store", ErrInvalid)
	}
	var status string
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT status FROM v_memory_status WHERE id = ?1 AND kind = 'skill' AND scope = 'user'`, []any{memoryID}, &status)
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return "", fmt.Errorf("%w: memory %d", ErrNotFound, memoryID)
	}
	if err != nil {
		return "", fmt.Errorf("skillstore status: %w", err)
	}
	return status, nil
}

// Search returns up to k approved skills. A text query is tokenised into
// terms and the last term is a prefix (SPEC §4.8 routing); an empty query
// lists approved skills by name. Quarantined rows are filtered through
// v_memory_status.
func Search(ctx context.Context, s *ctxdb.Store, text string, k int) ([]Row, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	var out []Row
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, ierr := searchIDs(tx, text, k)
		if ierr != nil {
			return ierr
		}
		out = make([]Row, 0, len(ids))
		for _, id := range ids {
			r, rerr := row(tx, id)
			if rerr != nil {
				return rerr
			}
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skillstore search: %w", err)
	}
	return out, nil
}

// Get returns existing rows by id (review/tests). It does not filter status.
func Get(ctx context.Context, s *ctxdb.Store, ids []int64) ([]Row, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	out := make([]Row, 0, len(ids))
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		for _, id := range ids {
			r, rerr := row(tx, id)
			if errors.Is(rerr, ctxdb.ErrNotFound) {
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
		return nil, fmt.Errorf("skillstore get: %w", err)
	}
	return out, nil
}

func indexTx(tx *ctxdb.Tx, r *Row, at int64, trusted bool) (int64, error) {
	if err := validate(r, true); err != nil {
		return 0, err
	}
	del := `DELETE FROM skills WHERE name = ?1 AND sha256 <> ?2`
	if !trusted {
		del += ` AND memory_id IS NOT NULL` // a quarantine never deletes a trusted row
	}
	if _, err := tx.Exec(del, r.Name, r.SHA256); err != nil {
		return 0, fmt.Errorf("skillstore reindex: %w", err)
	}
	var mem any
	if r.MemoryID > 0 {
		mem = r.MemoryID
	}
	var id int64
	err := tx.Get(`INSERT INTO skills(name, path, sha256, description, body, source, memory_id, indexed_at, requires_host)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)
		ON CONFLICT(name, sha256) DO UPDATE SET path = excluded.path, description = excluded.description,
			body = excluded.body, source = excluded.source, memory_id = excluded.memory_id,
			indexed_at = excluded.indexed_at, requires_host = excluded.requires_host
		RETURNING id`,
		[]any{r.Name, r.Path, r.SHA256, r.Description, r.Body, r.Source, mem, at, r.RequiresHost}, &id)
	if err != nil {
		return 0, fmt.Errorf("skillstore upsert: %w", err)
	}
	r.ID = id
	return id, nil
}

func quarantineMemory(tx *ctxdb.Tx, r *Row, scan string, rejected bool, at int64) (int64, error) {
	if err := validate(r, false); err != nil {
		return 0, err
	}
	if scan == "" {
		scan = "{}"
	}
	encID, ierr := newEncID()
	if ierr != nil {
		return 0, ierr
	}
	contentEnc, contentStored, serr := tx.SealRow("memory.content", "user|"+encID, r.Body, "")
	if serr != nil {
		return 0, fmt.Errorf("skillstore seal: %w", serr)
	}
	var id int64
	err := tx.Get(`INSERT INTO memory(kind, category, scope, name, content, content_enc, content_len, enc_id, origin, source_session,
		evidence_event_id, supersedes_id, expires_at, scan_result, created_at)
		VALUES ('skill', 'fact', 'user', ?1, ?2, ?3, ?4, ?5, ?6, NULL, NULL, NULL, NULL, ?7, ?8) RETURNING id`,
		[]any{r.Name, contentStored, nullBytes(contentEnc), utf8.RuneCountInString(r.Body), encID, r.Source, scan, at}, &id)
	if err != nil {
		return 0, fmt.Errorf("skillstore memory: %w", err)
	}
	status, reason := "scanned", "scan:review"
	if rejected {
		status, reason = "rejected", "scan:high"
	}
	_, err = tx.Exec(`INSERT INTO memory_decisions(memory_id, status, actor, reason, created_at)
		VALUES (?1, ?2, 'system', ?3, ?4)`, id, status, reason, at)
	if err != nil {
		return 0, fmt.Errorf("skillstore decision: %w", err)
	}
	return id, nil
}

func searchIDs(tx *ctxdb.Tx, text string, k int) ([]int64, error) {
	if k <= 0 {
		k = 3
	}
	query := ftsQuery(text)
	if query != "" {
		query = tx.FTSQuery(query)
	}
	if query == "" && strings.TrimSpace(text) != "" {
		return nil, nil
	}
	from := `skills s LEFT JOIN v_memory_status m ON m.id = s.memory_id`
	where := `((s.memory_id IS NULL AND s.source IN ('user', 'builtin')) OR (m.status = 'approved' AND m.kind = 'skill'))`
	where += ` AND NOT EXISTS (SELECT 1 FROM skills t WHERE t.name = s.name AND t.memory_id IS NULL AND t.id <> s.id)`
	order := "s.name"
	args := []any{k}
	if query != "" {
		from = `skills_fts f
			JOIN skills s ON s.id = f.rowid
			LEFT JOIN v_memory_status m ON m.id = s.memory_id`
		where += " AND skills_fts MATCH ?2"
		order = "bm25(skills_fts), s.name"
		args = append(args, query)
	}
	ids, err := tx.Ints("SELECT s.id FROM "+from+" WHERE "+where+" ORDER BY "+order+" LIMIT ?1", args...)
	if err != nil {
		return nil, fmt.Errorf("skillstore ids: %w", err)
	}
	return ids, nil
}

func row(tx *ctxdb.Tx, id int64) (Row, error) {
	var r Row
	var mem int64
	err := tx.Get(`SELECT id, name, description, body, path, sha256, source, COALESCE(memory_id, 0), requires_host
		FROM skills WHERE id = ?1`, []any{id}, &r.ID, &r.Name, &r.Description, &r.Body, &r.Path, &r.SHA256, &r.Source, &mem, &r.RequiresHost)
	if err != nil {
		return Row{}, err //nolint:wrapcheck // the exported boundary adds context; Get maps ErrNotFound
	}
	r.MemoryID = mem
	return r, nil
}

// redacted applies the built-in secret patterns before any storage or FTS
// write (JN3-4); the composition root additionally redacts configured secrets
// in skills.Service.
func redacted(r *Row) *Row {
	red := redact.New(redact.Options{})
	out := *r
	out.Description = red.String(r.Description)
	out.Body = red.String(r.Body)
	out.Path = red.String(r.Path)
	return &out
}

func newEncID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("skillstore enc id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func validate(r *Row, indexed bool) error {
	if r == nil {
		return fmt.Errorf("%w: nil row", ErrInvalid)
	}
	if r.Name == "" || r.Description == "" || r.Path == "" {
		return fmt.Errorf("%w: empty name/description/path", ErrInvalid)
	}
	if len(r.SHA256) != 64 {
		return fmt.Errorf("%w: sha256", ErrInvalid)
	}
	switch r.Source {
	case "user", "builtin", "hub", "agent", "import":
	default:
		return fmt.Errorf("%w: source %q", ErrInvalid, r.Source)
	}
	if len(r.Body) > MaxBodyBytes {
		return fmt.Errorf("%w: body > %d bytes", ErrInvalid, MaxBodyBytes)
	}
	if len(r.RequiresHost) > MaxRequiresBytes {
		return fmt.Errorf("%w: requires_host > %d bytes", ErrInvalid, MaxRequiresBytes)
	}
	if indexed && r.Body == "" {
		return fmt.Errorf("%w: empty body", ErrInvalid)
	}
	return nil
}

// ftsQuery is a conservative FTS5 expression with a prefix on the last term
// (routing keyword/prefix, SPEC §4.8); operators in free text become
// separators so a query can never fail with a MATCH syntax error.
func ftsQuery(text string) string {
	terms := strings.Fields(ctxdb.FTSNorm(text))
	if len(terms) == 0 {
		return ""
	}
	for i := range terms {
		term := strings.ReplaceAll(terms[i], `"`, `""`)
		if i == len(terms)-1 {
			terms[i] = `"` + term + `"*`
			continue
		}
		terms[i] = `"` + term + `"`
	}
	return strings.Join(terms, " ")
}

// SourceOf returns the winning stored source for name. A non-agent row wins
// so an agent-authored skill can never shadow or overwrite a trusted one.
func SourceOf(ctx context.Context, s *ctxdb.Store, name string) (string, bool, error) {
	var source string
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT source FROM skills WHERE name = ?1 ORDER BY CASE WHEN source = 'agent' THEN 1 ELSE 0 END, id LIMIT 1`, []any{name}, &source)
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("skillstore source: %w", err)
	}
	return source, true, nil
}

// Delete removes agent-authored rows by name; trusted/builtin rows are never
// deleted by the agent tool. Missing means ErrNotFound.
func Delete(ctx context.Context, s *ctxdb.Store, name string) error {
	var n int64
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		n, xerr = tx.Exec(`DELETE FROM skills WHERE name = ?1 AND source = 'agent'`, name)
		return xerr //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("skillstore delete: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: skill %s", ErrNotFound, name)
	}
	return nil
}

// ByName resolves one approved skill by its exact name. It is an indexed
// lookup, not an FTS top-k search, so it cannot miss a skill among many
// similarly named rows. The trusted-row shadow filter is the same as Search.
func ByName(ctx context.Context, s *ctxdb.Store, name string) (Row, bool, error) {
	var out Row
	found := false
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, ierr := tx.Ints(`SELECT id FROM skills WHERE name = ?1
			ORDER BY CASE WHEN memory_id IS NULL THEN 0 ELSE 1 END, id`, name)
		if ierr != nil {
			return fmt.Errorf("ids: %w", ierr)
		}
		for _, id := range ids {
			r, rerr := row(tx, id)
			if rerr != nil {
				return rerr
			}
			ok, aerr := approvedRow(tx, &r)
			if aerr != nil {
				return aerr
			}
			if ok {
				out, found = r, true
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return Row{}, false, fmt.Errorf("skillstore by name: %w", err)
	}
	return out, found, nil
}

func approvedRow(tx *ctxdb.Tx, r *Row) (bool, error) {
	if r.MemoryID == 0 {
		return r.Source == "user" || r.Source == "builtin", nil
	}
	var status string
	err := tx.Get(`SELECT status FROM v_memory_status WHERE id = ?1 AND kind = 'skill'`, []any{r.MemoryID}, &status)
	if errors.Is(err, ctxdb.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("status: %w", err)
	}
	return status == "approved", nil
}

// Replace points the newly installed agent row at path and removes the older
// agent rows with the same name. It runs after the directory swap; a failure
// leaves the previous rows untouched for the caller to roll back.
func Replace(ctx context.Context, s *ctxdb.Store, name, path string, keepID int64) error {
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		n, err := tx.Exec(`UPDATE skills SET path = ?2 WHERE id = ?1 AND source = 'agent'`, keepID, path)
		if err != nil {
			return fmt.Errorf("replace skill path: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: row %d", ErrNotFound, keepID)
		}
		if _, err := tx.Exec(`DELETE FROM skills WHERE name = ?1 AND source = 'agent' AND id <> ?2`, name, keepID); err != nil {
			return fmt.Errorf("replace skill rows: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("skillstore replace: %w", err)
	}
	return nil
}

// DeleteID removes one agent row (a staging install that could not be
// swapped). Trusted rows are never touched.
func DeleteID(ctx context.Context, s *ctxdb.Store, id int64) error {
	var n int64
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		n, xerr = tx.Exec(`DELETE FROM skills WHERE id = ?1 AND source = 'agent'`, id)
		return xerr //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("skillstore delete id: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: row %d", ErrNotFound, id)
	}
	return nil
}

// AgentPath returns the stored path of the newest agent row for name, even
// while it is still quarantined. Trusted rows are never returned.
func AgentPath(ctx context.Context, s *ctxdb.Store, name string) (string, bool, error) {
	var path string
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT path FROM skills WHERE name = ?1 AND source = 'agent' ORDER BY id DESC LIMIT 1`, []any{name}, &path)
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("skillstore agent path: %w", err)
	}
	return path, true, nil
}

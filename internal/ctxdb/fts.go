package ctxdb

import (
	"context"
	"database/sql"
	"fmt"
)

// FTS index versions (GAP-INF-02). Bumping either forces an automatic
// re-index; rowids are keyset-paged in batches so writers are not starved.
const (
	ftsNormVersion   = 1
	ftsLayoutVersion = 1
	ftsBatchSize     = 5000
	ftsBlobMaxBytes  = 16 << 20
)

func ftsTables() []string {
	return []string{"events_fts", "summaries_fts", "memory_fts", "skills_fts", "blob_fts"}
}

// ensureFTS initialises fts_meta and re-indexes when a binary with newer
// normalization/layout runs against an existing database.
func (s *Store) ensureFTS(ctx context.Context) error {
	if s.opt.ReadOnly {
		return nil
	}
	var n int
	if err := s.get(ctx, `SELECT count(*) FROM fts_meta`, nil, &n); err != nil {
		return err
	}
	if n == 0 {
		return s.Write(ctx, func(tx *Tx) error {
			for _, name := range ftsTables() {
				if err := tx.ftsTableMeta(name); err != nil {
					return err
				}
			}
			return nil
		})
	}
	var old int
	if err := s.get(ctx, `SELECT count(*) FROM fts_meta WHERE norm_version <> ? OR layout_version <> ?`,
		[]any{ftsNormVersion, ftsLayoutVersion}, &old); err != nil {
		return err
	}
	if old == 0 {
		return nil
	}
	return s.ReindexFTS(ctx, ftsBatchSize)
}

// ReindexFTS rebuilds every contentless FTS index in keyset-paged batches
// and records progress in fts_meta (EC-STATE-009, GAP-INF-02). It is
// idempotent and safe to resume after a crash: a new call clears and
// re-indexes from fts_meta.reindexed_to.
func (s *Store) ReindexFTS(ctx context.Context, batch int) error {
	if batch <= 0 {
		batch = ftsBatchSize
	}
	for _, name := range ftsTables() {
		if err := s.clearFTS(ctx, name); err != nil {
			return err
		}
	}
	if err := s.reindexEvents(ctx, batch); err != nil {
		return err
	}
	if err := s.reindexSummaries(ctx, batch); err != nil {
		return err
	}
	if err := s.reindexMemory(ctx, batch); err != nil {
		return err
	}
	if err := s.reindexSkills(ctx, batch); err != nil {
		return err
	}
	return s.reindexBlobs(ctx, batch)
}

type ftsEntry struct {
	id   int64
	cols []string
}

// ftsTableMeta initialises one index version row.
func (t *Tx) ftsTableMeta(name string) error {
	_, err := t.Exec(`INSERT INTO fts_meta(name, norm_version, layout_version, reindexed_to, updated_at)
		VALUES (?,?,?,0,?) ON CONFLICT(name) DO UPDATE SET norm_version=excluded.norm_version,
		layout_version=excluded.layout_version, updated_at=excluded.updated_at`,
		name, ftsNormVersion, ftsLayoutVersion, t.s.clk.WallMs())
	return err
}

func (t *Tx) ftsMetaProgress(name string, last int64) error {
	_, err := t.Exec(`UPDATE fts_meta SET norm_version=?, layout_version=?, reindexed_to=?, updated_at=? WHERE name=?`,
		ftsNormVersion, ftsLayoutVersion, last, t.s.clk.WallMs(), name)
	return err
}

func (s *Store) clearFTS(ctx context.Context, name string) error {
	q := map[string]string{
		"events_fts":    "INSERT INTO events_fts(events_fts) VALUES('delete-all')",
		"summaries_fts": "INSERT INTO summaries_fts(summaries_fts) VALUES('delete-all')",
		"memory_fts":    "INSERT INTO memory_fts(memory_fts) VALUES('delete-all')",
		"skills_fts":    "INSERT INTO skills_fts(skills_fts) VALUES('delete-all')",
		"blob_fts":      "INSERT INTO blob_fts(blob_fts) VALUES('delete-all')",
	}[name]
	if q == "" {
		return ErrInternal
	}
	return s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE fts_meta SET reindexed_to=0 WHERE name=?`, name)
		return err
	})
}

func (s *Store) ftsTokens(scope, identity string, enc []byte, plain string) (string, error) {
	if len(enc) == 0 {
		if s.codec == nil {
			return ftsNorm(plain), nil
		}
		return encFTS(s.codec, plain), nil
	}
	pt, err := s.openRow(scope, identity, enc, plain)
	if err != nil {
		return "", err
	}
	return encFTS(s.codec, pt), nil
}

func (s *Store) ftsPage(ctx context.Context, q string, last int64, batch int, scan func(*sql.Rows) (ftsEntry, error)) ([]ftsEntry, error) {
	args := []any{last, batch}
	var out []ftsEntry
	err := s.each(ctx, q, args, func(r *sql.Rows) error {
		e, err := scan(r)
		if err == nil {
			out = append(out, e)
		}
		return err
	})
	return out, err
}

func (s *Store) insertFTSBatch(ctx context.Context, table string, entries []ftsEntry) error {
	return s.Write(ctx, func(tx *Tx) error {
		for i := range entries {
			if err := insertFTSEntry(tx, table, entries[i]); err != nil {
				return err
			}
		}
		return tx.ftsMetaProgress(table, entries[len(entries)-1].id)
	})
}

func insertFTSEntry(tx *Tx, table string, e ftsEntry) error {
	var q string
	switch table {
	case "events_fts":
		q = `INSERT INTO events_fts(rowid, content, tool_name) VALUES (?,?,?)`
	case "summaries_fts", "memory_fts", "blob_fts":
		q = `INSERT INTO ` + table + `(rowid, content) VALUES (?,?)`
	case "skills_fts":
		q = `INSERT INTO skills_fts(rowid, name, description, body) VALUES (?,?,?,?)`
	default:
		return ErrInternal
	}
	args := make([]any, 0, 4)
	args = append(args, e.id)
	for _, c := range e.cols {
		args = append(args, c)
	}
	_, err := tx.c.ExecContext(tx.ctx, q, args...)
	return wrap(err)
}

func (s *Store) reindexEvents(ctx context.Context, batch int) error {
	const q = `SELECT id, session_id, seq, content, content_enc, coalesce(tool_name,'') FROM events
		WHERE kind IN ('user_message','assistant_message','tool_result','imported') AND id > ? ORDER BY id LIMIT ?`
	for last := int64(0); ; {
		rows, err := s.ftsPage(ctx, q, last, batch, func(r *sql.Rows) (ftsEntry, error) {
			var id, seq int64
			var sid, content, tool string
			var enc []byte
			if err := r.Scan(&id, &sid, &seq, &content, &enc, &tool); err != nil {
				return ftsEntry{}, wrap(err)
			}
			tokens, err := s.ftsTokens("events.content", fmt.Sprintf("%s|%d", sid, seq), enc, content)
			if err != nil {
				return ftsEntry{}, wrap(err)
			}
			return ftsEntry{id: id, cols: []string{tokens, s.ftsPlain(tool)}}, nil
		})
		if err != nil || len(rows) == 0 {
			return err
		}
		if err := s.insertFTSBatch(ctx, "events_fts", rows); err != nil {
			return err
		}
		last = rows[len(rows)-1].id
	}
}

func (s *Store) reindexSummaries(ctx context.Context, batch int) error {
	const q = `SELECT id, session_id, from_seq, to_seq, content, content_enc FROM summaries
		WHERE id > ? ORDER BY id LIMIT ?`
	for last := int64(0); ; {
		rows, err := s.ftsPage(ctx, q, last, batch, func(r *sql.Rows) (ftsEntry, error) {
			var id, from, to int64
			var sid, content string
			var enc []byte
			if err := r.Scan(&id, &sid, &from, &to, &content, &enc); err != nil {
				return ftsEntry{}, wrap(err)
			}
			tokens, err := s.ftsTokens("summaries.content", fmt.Sprintf("%s|%d:%d", sid, from, to), enc, content)
			return ftsEntry{id: id, cols: []string{tokens}}, err
		})
		if err != nil || len(rows) == 0 {
			return err
		}
		if err := s.insertFTSBatch(ctx, "summaries_fts", rows); err != nil {
			return err
		}
		last = rows[len(rows)-1].id
	}
}

func (s *Store) reindexMemory(ctx context.Context, batch int) error {
	const q = `SELECT id, scope, enc_id, content, content_enc FROM memory WHERE id > ? ORDER BY id LIMIT ?`
	for last := int64(0); ; {
		rows, err := s.ftsPage(ctx, q, last, batch, func(r *sql.Rows) (ftsEntry, error) {
			var id int64
			var scope, encID, content string
			var enc []byte
			if err := r.Scan(&id, &scope, &encID, &content, &enc); err != nil {
				return ftsEntry{}, wrap(err)
			}
			tokens, err := s.ftsTokens("memory.content", scope+"|"+encID, enc, content)
			return ftsEntry{id: id, cols: []string{tokens}}, err
		})
		if err != nil || len(rows) == 0 {
			return err
		}
		if err := s.insertFTSBatch(ctx, "memory_fts", rows); err != nil {
			return err
		}
		last = rows[len(rows)-1].id
	}
}

func (s *Store) reindexSkills(ctx context.Context, batch int) error {
	const q = `SELECT id, name, description, body FROM skills WHERE id > ? ORDER BY id LIMIT ?`
	for last := int64(0); ; {
		rows, err := s.ftsPage(ctx, q, last, batch, func(r *sql.Rows) (ftsEntry, error) {
			var id int64
			var name, desc, body string
			if err := r.Scan(&id, &name, &desc, &body); err != nil {
				return ftsEntry{}, wrap(err)
			}
			return ftsEntry{id: id, cols: []string{s.ftsPlain(name), s.ftsPlain(desc), s.ftsPlain(body)}}, nil
		})
		if err != nil || len(rows) == 0 {
			return err
		}
		if err := s.insertFTSBatch(ctx, "skills_fts", rows); err != nil {
			return err
		}
		last = rows[len(rows)-1].id
	}
}

func (s *Store) reindexBlobs(ctx context.Context, batch int) error {
	const q = `SELECT e.id, b.sha256 FROM events e JOIN blobs b ON b.sha256 = e.blob_sha
		WHERE e.id > ? AND b.size <= 16777216 AND (b.mime LIKE 'text/%' OR b.mime = 'application/json')
		ORDER BY e.id LIMIT ?`
	for last := int64(0); ; {
		rows, err := s.ftsPage(ctx, q, last, batch, func(r *sql.Rows) (ftsEntry, error) {
			var id int64
			var sha string
			if err := r.Scan(&id, &sha); err != nil {
				return ftsEntry{}, wrap(err)
			}
			return ftsEntry{id: id, cols: []string{sha}}, nil
		})
		if err != nil || len(rows) == 0 {
			return err
		}
		for i := range rows {
			raw, rerr := s.ReadBlob(ctx, rows[i].cols[0])
			if rerr != nil {
				return rerr
			}
			rows[i].cols = []string{s.ftsPlain(string(raw))}
		}
		if err := s.insertFTSBatch(ctx, "blob_fts", rows); err != nil {
			return err
		}
		last = rows[len(rows)-1].id
	}
}

func (s *Store) ftsPlain(text string) string {
	if s.codec == nil {
		return ftsNorm(text)
	}
	return encFTS(s.codec, text)
}

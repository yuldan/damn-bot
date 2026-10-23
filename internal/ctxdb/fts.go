package ctxdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	ftsNormVersion  = 1
	ftsPlainLayout  = 1 // detail=full, CJK unigrams
	ftsEncLayout    = 2 // detail=column, bigrams only (ADR-124 amendment)
	ftsBatchSize    = 5000
	ftsBlobMaxBytes = 16 << 20
)

func ftsTables() []string {
	return []string{"events_fts", "summaries_fts", "memory_fts", "skills_fts", "blob_fts"}
}

func (s *Store) ftsLayout() int {
	if s.codec != nil {
		return ftsEncLayout
	}
	return ftsPlainLayout
}

func (s *Store) ensureFTS(ctx context.Context) error {
	if s.opt.ReadOnly {
		return nil
	}
	var n int
	if err := s.get(ctx, `SELECT count(*) FROM fts_meta`, nil, &n); err != nil {
		return err
	}
	if n == 0 {
		return s.initFTS(ctx)
	}
	return s.upgradeFTS(ctx)
}

func (s *Store) initFTS(ctx context.Context) error {
	if s.codec != nil {
		if err := s.recreateEncryptedFTS(ctx); err != nil {
			return err
		}
	}
	return s.Write(ctx, func(tx *Tx) error {
		for _, name := range ftsTables() {
			if err := tx.ftsTableMeta(name); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) upgradeFTS(ctx context.Context) error {
	recreated, err := s.upgradeEncryptedLayout(ctx)
	if err != nil {
		return err
	}
	var old int
	if err := s.get(ctx, `SELECT count(*) FROM fts_meta WHERE norm_version <> ? OR layout_version <> ?`,
		[]any{ftsNormVersion, s.ftsLayout()}, &old); err != nil {
		return err
	}
	if old == 0 && !recreated {
		return nil
	}
	return s.ReindexFTS(ctx, ftsBatchSize)
}

// upgradeEncryptedLayout recreates the FTS tables when any of them still has
// the plaintext layout. Every table is inspected (SEC7-73): checking only
// events_fts let a partially converted database keep the four other indexes
// at detail=full, which breaks NEAR/position semantics.
func (s *Store) upgradeEncryptedLayout(ctx context.Context) (bool, error) {
	if s.codec == nil {
		return false, nil
	}
	ddl, err := s.strings(ctx, `SELECT sql FROM sqlite_master WHERE name IN
		('events_fts','summaries_fts','memory_fts','skills_fts','blob_fts')`)
	if err != nil {
		return false, err
	}
	if len(ddl) == len(ftsTables()) {
		columnDetail := true
		for _, q := range ddl {
			if !strings.Contains(q, "detail=column") {
				columnDetail = false
				break
			}
		}
		if columnDetail {
			return false, nil
		}
	}
	return true, s.recreateEncryptedFTS(ctx)
}

// ReindexFTS rebuilds contentless FTS indexes in keyset batches (EC-STATE-009).
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

func (t *Tx) ftsTableMeta(name string) error {
	_, err := t.Exec(`INSERT INTO fts_meta(name, norm_version, layout_version, reindexed_to, updated_at)
		VALUES (?,?,?,0,?) ON CONFLICT(name) DO UPDATE SET norm_version=excluded.norm_version,
		layout_version=excluded.layout_version, updated_at=excluded.updated_at`,
		name, ftsNormVersion, t.s.ftsLayout(), t.s.clk.WallMs())
	return err
}

func (t *Tx) ftsMetaProgress(name string, last int64) error {
	_, err := t.Exec(`UPDATE fts_meta SET norm_version=?, layout_version=?, reindexed_to=?, updated_at=? WHERE name=?`,
		ftsNormVersion, t.s.ftsLayout(), last, t.s.clk.WallMs(), name)
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
		s.hasBlobFTS.Store(true)
		last = rows[len(rows)-1].id
	}
}

func (t *Tx) indexBlobFTS(sha string, eventID int64) error {
	var mime string
	var size int64
	if err := t.Get(`SELECT mime, size FROM blobs WHERE sha256=?`, []any{sha}, &mime, &size); err != nil {
		return err
	}
	if size > ftsBlobMaxBytes || (mime != "application/json" && !strings.HasPrefix(mime, "text/")) {
		return nil
	}
	raw, err := t.s.root.ReadFile(blobName(sha))
	if err != nil {
		return wrap(err)
	}
	plain := raw
	if t.s.codec != nil {
		plain, err = t.s.codec.Open("blob:"+sha, raw)
		if err != nil {
			return wrap(err)
		}
	}
	_, err = t.Exec(`INSERT INTO blob_fts(rowid, content) VALUES (?,?)`, eventID, t.s.ftsPlain(string(plain)))
	if err == nil {
		t.s.hasBlobFTS.Store(true)
	}
	return err
}

func (s *Store) blobFTSAvailable(ctx context.Context) bool {
	s.blobOnce.Do(func() {
		var n int
		if err := s.get(ctx, `SELECT EXISTS(SELECT 1 FROM blob_fts)`, nil, &n); err == nil {
			s.hasBlobFTS.Store(n != 0)
		}
	})
	return s.hasBlobFTS.Load()
}

func (s *Store) ftsPlain(text string) string {
	if s.codec == nil {
		return ftsNorm(text)
	}
	return encFTS(s.codec, text)
}

func (s *Store) recreateEncryptedFTS(ctx context.Context) error {
	ddl := []string{
		"DROP TRIGGER IF EXISTS events_ai", "DROP TRIGGER IF EXISTS summaries_ai", "DROP TRIGGER IF EXISTS memory_ai",
		"DROP TRIGGER IF EXISTS skills_ai", "DROP TRIGGER IF EXISTS skills_au", "DROP TRIGGER IF EXISTS skills_ad",
		"DROP TABLE IF EXISTS events_fts", "DROP TABLE IF EXISTS summaries_fts", "DROP TABLE IF EXISTS memory_fts",
		"DROP TABLE IF EXISTS skills_fts", "DROP TABLE IF EXISTS blob_fts",
		`CREATE VIRTUAL TABLE events_fts USING fts5(content, tool_name, content='', contentless_delete=1, detail=column, tokenize = "unicode61 remove_diacritics 2 tokenchars '_-./'")`,
		`CREATE VIRTUAL TABLE summaries_fts USING fts5(content, content='', contentless_delete=1, detail=column, tokenize = "unicode61 remove_diacritics 2")`,
		`CREATE VIRTUAL TABLE memory_fts USING fts5(content, content='', contentless_delete=1, detail=column, tokenize = "unicode61 remove_diacritics 2")`,
		`CREATE VIRTUAL TABLE skills_fts USING fts5(name, description, body, content='', contentless_delete=1, detail=column, tokenize = "unicode61 remove_diacritics 2")`,
		`CREATE VIRTUAL TABLE blob_fts USING fts5(content, content='', contentless_delete=1, detail=column, tokenize = "unicode61 remove_diacritics 2 tokenchars '_-./'")`,
		`CREATE TRIGGER events_ai AFTER INSERT ON events WHEN NEW.kind IN ('user_message','assistant_message','tool_result','imported') BEGIN
			INSERT INTO events_fts(rowid, content, tool_name) VALUES (NEW.id,
			dmb_fts_norm(coalesce(NEW.content_enc, NEW.content), 'events.content', NEW.session_id || '|' || NEW.seq), coalesce(NEW.tool_name, '')); END`,
		`CREATE TRIGGER summaries_ai AFTER INSERT ON summaries BEGIN INSERT INTO summaries_fts(rowid, content)
			VALUES (NEW.id, dmb_fts_norm(coalesce(NEW.content_enc, NEW.content), 'summaries.content', NEW.session_id || '|' || NEW.from_seq || ':' || NEW.to_seq)); END`,
		`CREATE TRIGGER memory_ai AFTER INSERT ON memory BEGIN INSERT INTO memory_fts(rowid, content)
			VALUES (NEW.id, dmb_fts_norm(coalesce(NEW.content_enc, NEW.content), 'memory.content', NEW.scope || '|' || NEW.enc_id)); END`,
		`CREATE TRIGGER skills_ai AFTER INSERT ON skills BEGIN INSERT INTO skills_fts(rowid, name, description, body)
			VALUES (NEW.id, dmb_fts_norm(NEW.name, 'skills.name', ''), dmb_fts_norm(NEW.description, 'skills.description', ''), dmb_fts_norm(NEW.body, 'skills.body', '')); END`,
		`CREATE TRIGGER skills_au AFTER UPDATE ON skills BEGIN DELETE FROM skills_fts WHERE rowid = OLD.id;
			INSERT INTO skills_fts(rowid, name, description, body) VALUES (NEW.id, dmb_fts_norm(NEW.name, 'skills.name', ''),
			dmb_fts_norm(NEW.description, 'skills.description', ''), dmb_fts_norm(NEW.body, 'skills.body', '')); END`,
		`CREATE TRIGGER skills_ad AFTER DELETE ON skills BEGIN DELETE FROM skills_fts WHERE rowid = OLD.id; END`,
	}
	return s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Exec("PRAGMA secure_delete=ON"); err != nil {
			return err
		}
		for _, q := range ddl {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
}

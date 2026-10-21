package ctxdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// encryptStaged converts a plaintext archive DB (and its staged blobs) to the
// encrypted representation before the files are installed. The staged copy is
// offline; its append-only triggers are rebuilt in the same transaction.
func encryptStaged(ctx context.Context, dbPath, blobsRoot string, codec Codec) error {
	already, err := encryptDB(ctx, dbPath, codec)
	if err != nil || already {
		return err
	}
	if blobsRoot != "" {
		return encryptBlobs(filepath.Join(blobsRoot, "blobs"), codec)
	}
	return nil
}

func encryptBlobs(dir string, codec Codec) error {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ctxdb: encrypt blobs: %w", wrap(err))
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		files, rerr := os.ReadDir(filepath.Join(dir, e.Name()))
		if rerr != nil {
			return fmt.Errorf("ctxdb: encrypt blobs: %w", wrap(rerr))
		}
		for _, f := range files {
			p := filepath.Join(dir, e.Name(), f.Name())
			if err := encryptBlob(p, codec); err != nil {
				return err
			}
		}
	}
	return nil
}

func encryptBlob(p string, codec Codec) error {
	plain, err := os.ReadFile(filepath.Clean(p))
	if err != nil {
		return fmt.Errorf("ctxdb: encrypt blob: %w", wrap(err))
	}
	sum := sha256.Sum256(plain)
	if hex.EncodeToString(sum[:]) != path.Base(p) {
		return fmt.Errorf("ctxdb: encrypt blob name: %w", ErrInternal)
	}
	sealed, err := codec.Seal("blob:"+path.Base(p), plain)
	if err != nil {
		return fmt.Errorf("ctxdb: encrypt blob: %w", wrap(err))
	}
	tmp := p + ".enc"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil { //nolint:gosec // staged blob path
		return fmt.Errorf("ctxdb: encrypt blob write: %w", wrap(err))
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("ctxdb: encrypt blob publish: %w", wrap(err))
	}
	return nil
}

type trigger struct{ name, sql string }

func encTables() []string {
	return []string{"events", "summaries", "memory", "reasoning_carriers", "gateway_inbound"}
}

func encryptDB(ctx context.Context, dbPath string, codec Codec) (already bool, err error) {
	db, err := sql.Open("sqlite", filepath.Clean(dbPath))
	if err != nil {
		return false, fmt.Errorf("ctxdb: encrypt db: %w", wrap(err))
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if _, jerr := db.ExecContext(ctx, "PRAGMA journal_mode=DELETE"); jerr != nil {
		return false, fmt.Errorf("ctxdb: encrypt journal: %w", wrap(jerr))
	}
	if _, serr := db.ExecContext(ctx, "PRAGMA secure_delete=ON"); serr != nil {
		return false, fmt.Errorf("ctxdb: encrypt secure_delete: %w", wrap(serr))
	}
	var id string
	qerr := db.QueryRowContext(ctx, `SELECT value FROM module_state WHERE module_id='storage' AND key='encrypt'`).Scan(&id)
	if qerr == nil {
		return true, nil
	}
	if !errors.Is(qerr, sql.ErrNoRows) {
		return false, fmt.Errorf("ctxdb: encrypt marker: %w", wrap(qerr))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("ctxdb: encrypt tx: %w", wrap(err))
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, wrap(rerr))
		}
	}()
	if err := convert(ctx, tx, codec); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, wrap(err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return false, fmt.Errorf("ctxdb: vacuum converted db: %w", wrap(err))
	}
	return false, nil
}

func convert(ctx context.Context, tx *sql.Tx, codec Codec) error {
	trigs, err := loadTriggers(ctx, tx)
	if err != nil {
		return err
	}
	for _, t := range trigs {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(t.name, `"`, `""`)+`"` /*nolint:gosec*/); err != nil {
			return fmt.Errorf("ctxdb: drop trigger: %w", wrap(err))
		}
	}
	if err := sealEvents(ctx, tx, codec); err != nil {
		return err
	}
	if err := sealSummaries(ctx, tx, codec); err != nil {
		return err
	}
	if err := sealMemory(ctx, tx, codec); err != nil {
		return err
	}
	if err := sealCarriers(ctx, tx, codec); err != nil {
		return err
	}
	if err := sealGateway(ctx, tx, codec); err != nil {
		return err
	}
	for _, fts := range []string{"events_fts", "summaries_fts", "memory_fts"} {
		q := `INSERT INTO ` + fts + `(` + fts + `) VALUES('delete-all')` //nolint:gosec // fixed table names
		if _, derr := tx.ExecContext(ctx, q); derr != nil {
			return fmt.Errorf("ctxdb: clear %s: %w", fts, wrap(derr))
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO module_state(module_id,key,value,updated_at) VALUES ('storage','encrypt',?,strftime('%s','now')*1000)`, codec.ID()); err != nil {
		return fmt.Errorf("ctxdb: encrypt marker: %w", wrap(err))
	}
	for _, t := range trigs {
		if _, err := tx.ExecContext(ctx, t.sql); err != nil {
			return fmt.Errorf("ctxdb: restore trigger %s: %w", t.name, wrap(err))
		}
	}
	return nil
}

func loadTriggers(ctx context.Context, tx *sql.Tx) (out []trigger, err error) {
	q := `SELECT name, sql FROM sqlite_master WHERE type='trigger' AND tbl_name IN (` + quotedList(encTables()) + `)` //nolint:gosec // fixed table names
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("ctxdb: load triggers: %w", wrap(err))
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var t trigger
		if err := rows.Scan(&t.name, &t.sql); err != nil {
			return nil, fmt.Errorf("ctxdb: load trigger scan: %w", wrap(err))
		}
		out = append(out, t)
	}
	return out, wrap(rows.Err())
}

func quotedList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "'" + x + "'"
	}
	return strings.Join(q, ",")
}

func sealEvents(ctx context.Context, tx *sql.Tx, codec Codec) (err error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, session_id, seq, kind, content, payload, coalesce(blob_sha,'')
		FROM events ORDER BY session_id, seq`)
	if err != nil {
		return fmt.Errorf("ctxdb: seal events: %w", wrap(err))
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	type row struct {
		id                            int64
		sid, kind, content, pay, blob string
		seq                           int64
	}
	var rs []row
	for rows.Next() {
		var r row
		if serr := rows.Scan(&r.id, &r.content, &r.pay); serr != nil {
			return fmt.Errorf("ctxdb: seal events scan: %w", wrap(serr))
		}
		rs = append(rs, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return fmt.Errorf("ctxdb: seal events rows: %w", wrap(rerr))
	}
	prev := map[string]string{}
	for _, r := range rs {
		id := r.sid + "|" + strconv.FormatInt(r.seq, 10)
		ce, err := codec.Seal("events.content:"+id, []byte(r.content))
		if err != nil {
			return wrap(err)
		}
		pe, err := codec.Seal("events.payload:"+id, []byte(r.pay))
		if err != nil {
			return wrap(err)
		}
		plain := strings.Join([]string{prev[r.sid], r.sid, strconv.FormatInt(r.seq, 10), r.kind, r.content, r.pay, r.blob}, "|")
		hash := codec.Hash(plain)
		prev[r.sid] = hash
		if _, err := tx.ExecContext(ctx, `UPDATE events SET content='', content_enc=?, payload='{}', payload_enc=?, hash=? WHERE id=?`, ce, pe, hash, r.id); err != nil {
			return fmt.Errorf("ctxdb: seal events update: %w", wrap(err))
		}
	}
	return nil
}

func sealSummaries(ctx context.Context, tx *sql.Tx, codec Codec) error {
	rs, err := scan2(ctx, tx, `SELECT id, content FROM summaries`)
	if err != nil {
		return err
	}
	for _, r := range rs {
		enc, err := codec.Seal("summaries.content", []byte(r.content))
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE summaries SET content='{}', content_enc=? WHERE id=?`, enc, r.id); err != nil {
			return fmt.Errorf("ctxdb: seal summaries: %w", wrap(err))
		}
	}
	return nil
}

func sealMemory(ctx context.Context, tx *sql.Tx, codec Codec) error {
	rs, err := scan2(ctx, tx, `SELECT id, content FROM memory`)
	if err != nil {
		return err
	}
	for _, r := range rs {
		enc, err := codec.Seal("memory.content", []byte(r.content))
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE memory SET content='', content_enc=?, content_len=length(?) WHERE id=?`, enc, r.content, r.id); err != nil {
			return fmt.Errorf("ctxdb: seal memory: %w", wrap(err))
		}
	}
	return nil
}

func sealCarriers(ctx context.Context, tx *sql.Tx, codec Codec) error {
	rs, err := scan2Blob(ctx, tx, `SELECT id, payload FROM reasoning_carriers`)
	if err != nil {
		return err
	}
	for _, r := range rs {
		enc, err := codec.Seal("reasoning_carriers.payload", r.content)
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE reasoning_carriers SET payload=X'', payload_enc=? WHERE id=?`, enc, r.id); err != nil {
			return fmt.Errorf("ctxdb: seal carriers: %w", wrap(err))
		}
	}
	return nil
}

func sealGateway(ctx context.Context, tx *sql.Tx, codec Codec) error {
	rs, err := scan2(ctx, tx, `SELECT rowid, payload FROM gateway_inbound WHERE payload IS NOT NULL`)
	if err != nil {
		return err
	}
	for _, r := range rs {
		enc, err := codec.Seal("gateway_inbound.payload", []byte(r.content))
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_inbound SET payload='{}', payload_enc=? WHERE rowid=?`, enc, r.id); err != nil {
			return fmt.Errorf("ctxdb: seal gateway: %w", wrap(err))
		}
	}
	return nil
}

type textRow struct {
	id      int64
	content string
}

func scan2(ctx context.Context, tx *sql.Tx, q string) (out []textRow, err error) { //nolint:dupl // same shape as scan2Blob
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("ctxdb: seal query: %w", wrap(err))
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r textRow
		if err := rows.Scan(&r.id, &r.content); err != nil {
			return nil, fmt.Errorf("ctxdb: seal scan: %w", wrap(err))
		}
		out = append(out, r)
	}
	return out, wrap(rows.Err())
}

type blobRow struct {
	id      int64
	content []byte
}

func scan2Blob(ctx context.Context, tx *sql.Tx, q string) (out []blobRow, err error) { //nolint:dupl // same shape as scan2
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("ctxdb: seal query: %w", wrap(err))
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r blobRow
		if err := rows.Scan(&r.id, &r.content); err != nil {
			return nil, fmt.Errorf("ctxdb: seal scan: %w", wrap(err))
		}
		out = append(out, r)
	}
	return out, wrap(rows.Err())
}

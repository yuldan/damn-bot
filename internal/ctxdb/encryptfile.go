package ctxdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type trigger struct{ name, sql string }

// encryptStaged converts an offline, verified staging DB and its blobs. The
// DB is first migrated with this binary's ladder; trigger SQL is read from a
// fresh binary-created schema, never from the archive.
func encryptStaged(ctx context.Context, dbPath, blobsRoot string, codec Codec) error {
	already, err := hasEncryptMarker(ctx, dbPath)
	if err != nil || already {
		return err
	}
	if merr := migrateStaged(ctx, dbPath); merr != nil {
		return merr
	}
	trigs, err := trustedTriggers(ctx)
	if err != nil {
		return err
	}
	if err := encryptDB(ctx, dbPath, codec, trigs); err != nil {
		return err
	}
	if blobsRoot == "" {
		return nil
	}
	return encryptBlobs(filepath.Join(blobsRoot, "blobs"), codec)
}

func hasEncryptMarker(ctx context.Context, dbPath string) (ok bool, err error) {
	db, err := sql.Open("sqlite", filepath.Clean(dbPath))
	if err != nil {
		return false, wrap(err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	var v string
	qerr := db.QueryRowContext(ctx, `SELECT value FROM module_state WHERE module_id='storage' AND key='encrypt'`).Scan(&v)
	switch {
	case qerr == nil:
		return true, nil
	case errors.Is(qerr, sql.ErrNoRows):
		return false, nil
	default:
		return false, wrap(qerr)
	}
}

func migrateStaged(ctx context.Context, dbPath string) (err error) {
	tmp, err := os.MkdirTemp(filepath.Dir(dbPath), ".migrate-")
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(tmp)) }()
	dst := filepath.Join(tmp, dbFile)
	if cerr := copyFile(dst, dbPath); cerr != nil {
		return cerr
	}
	s, err := Open(ctx, &Options{Dir: tmp, Kind: "cli"})
	if err != nil {
		return err
	}
	if cerr := s.CloseContext(ctx); cerr != nil {
		return cerr
	}
	return copyFile(dbPath, dst)
}

func copyFile(dst, src string) (err error) {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return wrap(err)
	}
	out, err := os.OpenFile(filepath.Clean(dst), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_, err = io.Copy(out, in)
	}
	if err == nil {
		err = out.Sync()
	}
	return wrap(errors.Join(err, out.Close(), in.Close()))
}

func trustedTriggers(ctx context.Context) (out []trigger, err error) {
	tmp, err := os.MkdirTemp("", "dmb-triggers-")
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(tmp)) }()
	s, err := Open(ctx, &Options{Dir: tmp, Kind: "cli"})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, s.CloseContext(ctx)) }()
	rows, err := s.r.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type='trigger' AND tbl_name IN (`+quotedList(encTables())+`)`) //nolint:gosec // fixed table names
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var t trigger
		if serr := rows.Scan(&t.name, &t.sql); serr != nil {
			return nil, wrap(serr)
		}
		out = append(out, t)
	}
	return out, wrap(rows.Err())
}

func encTables() []string {
	return []string{"events", "summaries", "memory", "reasoning_carriers", "gateway_inbound"}
}

func quotedList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "'" + x + "'"
	}
	return strings.Join(q, ",")
}

func encryptDB(ctx context.Context, dbPath string, codec Codec, trigs []trigger) (err error) {
	db, err := sql.Open("sqlite", filepath.Clean(dbPath))
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if _, jerr := db.ExecContext(ctx, "PRAGMA journal_mode=DELETE"); jerr != nil {
		return wrap(jerr)
	}
	if _, serr := db.ExecContext(ctx, "PRAGMA secure_delete=ON"); serr != nil {
		return wrap(serr)
	}
	if ferr := rebuildFTS(ctx, db, codec); ferr != nil {
		return ferr
	}
	tx, terr := db.BeginTx(ctx, nil)
	if terr != nil {
		return wrap(terr)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, wrap(rerr))
		}
	}()
	if err := convertTables(ctx, tx, codec, trigs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrap(err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return wrap(err)
	}
	return nil
}

func convertTables(ctx context.Context, tx *sql.Tx, codec Codec, trigs []trigger) error {
	for _, t := range trigs {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(t.name, `"`, `""`)+`"`); err != nil {
			return wrap(err)
		}
	}
	for _, seal := range []func(context.Context, *sql.Tx, Codec) error{
		sealEvents, sealSummaries, sealMemory, sealCarriers, sealGateway,
	} {
		if err := seal(ctx, tx, codec); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO module_state(module_id,key,value,updated_at) VALUES ('storage','encrypt',?,strftime('%s','now')*1000)`, codec.ID()); err != nil {
		return wrap(err)
	}
	for _, t := range trigs {
		if _, err := tx.ExecContext(ctx, t.sql); err != nil {
			return fmt.Errorf("ctxdb: recreate trigger %s: %w", t.name, wrap(err))
		}
	}
	return nil
}

func sealEvents(ctx context.Context, tx *sql.Tx, codec Codec) (err error) {
	type row struct {
		id                          int64
		sid, kind, content, payload string
		blob                        string
		seq                         int64
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, session_id, seq, kind, content, payload, coalesce(blob_sha,'') FROM events ORDER BY session_id, seq`)
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.sid, &r.seq, &r.kind, &r.content, &r.payload, &r.blob); err != nil {
			return wrap(err)
		}
		rs = append(rs, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return wrap(rerr)
	}
	if err := rows.Close(); err != nil {
		return wrap(err)
	}
	prev := map[string]string{}
	for _, r := range rs {
		id := r.sid + "|" + strconv.FormatInt(r.seq, 10)
		ce, err := codec.Seal("events.content", []byte(id+"\x00"+r.content))
		if err != nil {
			return wrap(err)
		}
		pe, err := codec.Seal("events.payload", []byte(id+"\x00"+r.payload))
		if err != nil {
			return wrap(err)
		}
		hash := codec.Hash(strings.Join([]string{prev[r.sid], r.sid, strconv.FormatInt(r.seq, 10), r.kind, r.content, r.payload, r.blob}, "|"))
		prev[r.sid] = hash
		if _, err := tx.ExecContext(ctx, `UPDATE events SET content='', content_enc=?, payload='{}', payload_enc=?, hash=? WHERE id=?`, ce, pe, hash, r.id); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func sealSummaries(ctx context.Context, tx *sql.Tx, codec Codec) (err error) {
	type row struct {
		id       int64
		sid      string
		from, to int64
		content  string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, session_id, from_seq, to_seq, content FROM summaries`)
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.sid, &r.from, &r.to, &r.content); err != nil {
			return wrap(err)
		}
		rs = append(rs, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return wrap(rerr)
	}
	if err := rows.Close(); err != nil {
		return wrap(err)
	}
	for _, r := range rs {
		identity := fmt.Sprintf("%s|%d:%d", r.sid, r.from, r.to)
		enc, err := codec.Seal("summaries.content", []byte(identity+"\x00"+r.content))
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE summaries SET content='{}', content_enc=? WHERE id=?`, enc, r.id); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func sealMemory(ctx context.Context, tx *sql.Tx, codec Codec) (err error) {
	type row struct {
		id      int64
		scope   string
		content string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, scope, content FROM memory`)
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.scope, &r.content); err != nil {
			return wrap(err)
		}
		rs = append(rs, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return wrap(rerr)
	}
	if err := rows.Close(); err != nil {
		return wrap(err)
	}
	for _, r := range rs {
		encID, err := newEncIDDB()
		if err != nil {
			return err
		}
		enc, err := codec.Seal("memory.content", []byte(r.scope+"|"+encID+"\x00"+r.content))
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE memory SET content='', content_enc=?, content_len=length(?), enc_id=? WHERE id=?`, enc, r.content, encID, r.id); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func sealCarriers(ctx context.Context, tx *sql.Tx, codec Codec) (err error) {
	type row struct {
		id           int64
		eventID, ord int64
		payload      []byte
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, event_id, ord, payload FROM reasoning_carriers`)
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.eventID, &r.ord, &r.payload); err != nil {
			return wrap(err)
		}
		rs = append(rs, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return wrap(rerr)
	}
	if err := rows.Close(); err != nil {
		return wrap(err)
	}
	for _, r := range rs {
		enc, err := codec.Seal("reasoning_carriers.payload", append([]byte(fmt.Sprintf("%d|%d\x00", r.eventID, r.ord)), r.payload...))
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE reasoning_carriers SET payload=X'', payload_enc=? WHERE id=?`, enc, r.id); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func sealGateway(ctx context.Context, tx *sql.Tx, codec Codec) (err error) {
	type row struct {
		id            int64
		platform, ext string
		payload       string
	}
	rows, err := tx.QueryContext(ctx, `SELECT rowid, platform, external_id, payload FROM gateway_inbound WHERE payload IS NOT NULL`)
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.platform, &r.ext, &r.payload); err != nil {
			return wrap(err)
		}
		rs = append(rs, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return wrap(rerr)
	}
	if err := rows.Close(); err != nil {
		return wrap(err)
	}
	for _, r := range rs {
		enc, err := codec.Seal("gateway_inbound.payload", []byte(r.platform+"|"+r.ext+"\x00"+r.payload))
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_inbound SET payload='{}', payload_enc=? WHERE rowid=?`, enc, r.id); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func rebuildFTS(ctx context.Context, db *sql.DB, codec Codec) error {
	for _, fts := range []string{"events_fts", "summaries_fts", "memory_fts"} {
		q := `INSERT INTO ` + fts + `(` + fts + `) VALUES('delete-all')` //nolint:gosec // fixed names
		if _, err := db.ExecContext(ctx, q); err != nil {
			return wrap(err)
		}
	}
	items := []struct{ q, fts string }{
		{`SELECT id, content FROM events WHERE kind IN ('user_message','assistant_message','tool_result','imported')`, "events_fts"},
		{`SELECT id, content FROM summaries`, "summaries_fts"},
		{`SELECT id, content FROM memory`, "memory_fts"},
	}
	for _, it := range items {
		if err := rebuildOne(ctx, db, it.q, it.fts, codec); err != nil {
			return err
		}
	}
	return nil
}

func rebuildOne(ctx context.Context, db *sql.DB, q, fts string, codec Codec) error {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return wrap(err)
	}
	type row struct {
		id      int64
		content string
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.content); err != nil {
			return wrap(err)
		}
		rs = append(rs, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return wrap(rerr)
	}
	if err := rows.Close(); err != nil {
		return wrap(err)
	}
	for _, r := range rs {
		q := `INSERT INTO ` + fts + `(rowid, content) VALUES (?,?)` //nolint:gosec // fixed names
		if _, err := db.ExecContext(ctx, q, r.id, encFTS(codec, r.content)); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func encFTS(codec Codec, s string) string {
	fields := strings.FieldsFunc(ftsNorm(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, codec.FTS(f))
	}
	return strings.Join(out, " ")
}

func encryptBlobs(dir string, codec Codec) error {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return wrap(err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		files, rerr := os.ReadDir(filepath.Join(dir, e.Name()))
		if rerr != nil {
			return wrap(rerr)
		}
		for _, f := range files {
			if err := encryptBlob(filepath.Join(dir, e.Name(), f.Name()), codec); err != nil {
				return err
			}
		}
	}
	return nil
}

func encryptBlob(p string, codec Codec) error {
	plain, err := os.ReadFile(filepath.Clean(p))
	if err != nil {
		return wrap(err)
	}
	sha := path.Base(p)
	sum := sha256.Sum256(plain)
	if hex.EncodeToString(sum[:]) != sha {
		return ErrInternal
	}
	sealed, err := codec.Seal("blob:"+sha, plain)
	if err != nil {
		return wrap(err)
	}
	tmp := p + ".enc"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil { //nolint:gosec // staging blob path
		return wrap(err)
	}
	return wrap(os.Rename(tmp, p))
}

func newEncIDDB() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", wrap(err)
	}
	return hex.EncodeToString(b[:]), nil
}

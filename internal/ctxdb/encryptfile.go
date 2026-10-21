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
	if ferr := clearFTS(ctx, db); ferr != nil {
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

func sealEvents(ctx context.Context, tx *sql.Tx, codec Codec) error {
	rs, err := queryRows(ctx, tx, `SELECT id, session_id, seq, kind, content, payload, coalesce(blob_sha,'') FROM events ORDER BY session_id, seq`)
	if err != nil {
		return err
	}
	prev := map[string]string{}
	for _, r := range rs {
		id := text(r[1]) + "|" + strconv.FormatInt(num(r[2]), 10)
		ce, err := codec.Seal("events.content", []byte(id+"\x00"+text(r[4])))
		if err != nil {
			return wrap(err)
		}
		pe, err := codec.Seal("events.payload", []byte(id+"\x00"+text(r[5])))
		if err != nil {
			return wrap(err)
		}
		hash := codec.Hash(strings.Join([]string{prev[text(r[1])], text(r[1]), strconv.FormatInt(num(r[2]), 10), text(r[3]), text(r[4]), text(r[5]), text(r[6])}, "|"))
		prev[text(r[1])] = hash
		if _, err := tx.ExecContext(ctx, `UPDATE events SET content='', content_enc=?, payload='{}', payload_enc=?, hash=? WHERE id=?`, ce, pe, hash, num(r[0])); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func sealSummaries(ctx context.Context, tx *sql.Tx, codec Codec) error {
	rs, err := queryRows(ctx, tx, `SELECT id, session_id, from_seq, to_seq, content FROM summaries`)
	if err != nil {
		return err
	}
	for _, r := range rs {
		identity := fmt.Sprintf("%s|%d:%d", text(r[1]), num(r[2]), num(r[3]))
		if err := sealText(ctx, tx, codec, "summaries.content", identity, text(r[4]), `UPDATE summaries SET content='{}', content_enc=? WHERE id=?`, num(r[0])); err != nil {
			return err
		}
	}
	return nil
}

func sealMemory(ctx context.Context, tx *sql.Tx, codec Codec) error {
	rs, err := queryRows(ctx, tx, `SELECT id, scope, content FROM memory`)
	if err != nil {
		return err
	}
	for _, r := range rs {
		encID, err := newEncIDDB()
		if err != nil {
			return err
		}
		identity := text(r[1]) + "|" + encID
		plain := text(r[2])
		enc, err := codec.Seal("memory.content", []byte(identity+"\x00"+plain))
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE memory SET content='', content_enc=?, content_len=length(?), enc_id=? WHERE id=?`, enc, plain, encID, num(r[0])); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func sealCarriers(ctx context.Context, tx *sql.Tx, codec Codec) error {
	rs, err := queryRows(ctx, tx, `SELECT id, event_id, ord, payload FROM reasoning_carriers`)
	if err != nil {
		return err
	}
	for _, r := range rs {
		identity := fmt.Sprintf("%d|%d", num(r[1]), num(r[2]))
		enc, err := codec.Seal("reasoning_carriers.payload", append([]byte(identity+"\x00"), bytesOf(r[3])...))
		if err != nil {
			return wrap(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE reasoning_carriers SET payload=X'', payload_enc=? WHERE id=?`, enc, num(r[0])); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func sealGateway(ctx context.Context, tx *sql.Tx, codec Codec) error {
	rs, err := queryRows(ctx, tx, `SELECT rowid, platform, external_id, payload FROM gateway_inbound WHERE payload IS NOT NULL`)
	if err != nil {
		return err
	}
	for _, r := range rs {
		identity := text(r[1]) + "|" + text(r[2])
		if err := sealText(ctx, tx, codec, "gateway_inbound.payload", identity, text(r[3]), `UPDATE gateway_inbound SET payload='{}', payload_enc=? WHERE rowid=?`, num(r[0])); err != nil {
			return err
		}
	}
	return nil
}

func sealText(ctx context.Context, tx *sql.Tx, codec Codec, scope, identity, plain, q string, id int64) error {
	enc, err := codec.Seal(scope, []byte(identity+"\x00"+plain))
	if err != nil {
		return wrap(err)
	}
	_, err = tx.ExecContext(ctx, q, enc, id)
	return wrap(err)
}

func queryRows(ctx context.Context, tx *sql.Tx, q string) (out [][]any, err error) {
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, wrap(err)
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if serr := rows.Scan(ptrs...); serr != nil {
			return nil, wrap(serr)
		}
		out = append(out, vals)
	}
	return out, wrap(rows.Err())
}

func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

func num(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	default:
		return 0
	}
}

func bytesOf(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	return []byte(text(v))
}

func clearFTS(ctx context.Context, db *sql.DB) error {
	for _, q := range []string{
		"INSERT INTO events_fts(events_fts) VALUES('delete-all')",
		"INSERT INTO summaries_fts(summaries_fts) VALUES('delete-all')",
		"INSERT INTO memory_fts(memory_fts) VALUES('delete-all')",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func encryptBlobs(dir string, codec Codec) error {
	return wrap(filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() {
			return err
		}
		return encryptBlob(p, codec)
	}))
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
	b := make([]byte, 8)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), wrap(err)
}

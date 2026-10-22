package ctxdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// ftsShadow returns the concatenated blocks of one FTS shadow table so a test
// can tell a keyed token ("zt…" with the one-way test codec) from a literal
// plaintext one (SEC7-71/72).
func ftsShadow(t *testing.T, dir, table string) string {
	t.Helper()
	q := map[string]string{
		"blob_fts_data":   `SELECT CAST(block AS TEXT) FROM blob_fts_data`,
		"skills_fts_data": `SELECT CAST(block AS TEXT) FROM skills_fts_data`,
	}[table]
	if q == "" {
		t.Fatalf("unknown FTS shadow table %q", table)
	}
	db, err := sql.Open("sqlite", filepath.Clean(DBPath(dir)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	rows, err := db.QueryContext(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	var b strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		b.WriteString(s)
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatal(rerr)
	}
	return b.String()
}

// ftsDDL returns the sqlite_master SQL of one FTS table.
func ftsDDL(t *testing.T, dir, table string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Clean(DBPath(dir)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	var ddl string
	if qerr := db.QueryRowContext(context.Background(),
		`SELECT sql FROM sqlite_master WHERE name=?`, table).Scan(&ddl); qerr != nil {
		t.Fatal(qerr)
	}
	return ddl
}

// seedBlobAndSkill writes one skill row and one spilled tool output whose FTS
// tokens can be recognised after the conversion.
func seedBlobAndSkill(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	session(t, s, "S", "")
	if werr := s.Write(ctx, func(tx *Tx) error {
		_, xerr := tx.Exec(`INSERT INTO skills(name,path,sha256,description,body,source,indexed_at)
			VALUES ('kanariskillnamexyz','/p','h','kanariskilldeskxyz','kanariskillbodyxyz','user',1)`)
		return xerr
	}); werr != nil {
		t.Fatal(werr)
	}
	blob, err := s.PutBlob([]byte("kanariblobpayloadxyz and more text"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if werr := s.Write(ctx, func(tx *Tx) error { return tx.Publish(blob) }); werr != nil {
		t.Fatal(werr)
	}
	if _, aerr := s.Append(ctx, "S", nil, Event{
		Kind: "tool_result", ToolName: "shell", Content: "out", Payload: "{}", Blob: blob.Sha,
	}); aerr != nil {
		t.Fatal(aerr)
	}
}

// TestEncryptStagedRebuildsEveryFTS (SEC7-71): the file produced by
// encryptStaged must carry keyed tokens for blob_fts and skills_fts as well.
// Before the fix clearFTS/rebuildFTS skipped both, so the plaintext words of a
// spilled tool output and of every skill stayed in the converted database
// until the next codec open.
func TestEncryptStagedRebuildsEveryFTS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p, err := Open(ctx, &Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	seedBlobAndSkill(t, p)
	if cerr := p.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if blobs := ftsShadow(t, dir, "blob_fts_data"); !strings.Contains(blobs, "kanariblobpayloadxyz") {
		t.Fatalf("plaintext phase: blob_fts_data lacks the literal token: %q", blobs)
	}
	if err := encryptStaged(ctx, DBPath(dir), dir, testCodec{id: "dek"}); err != nil {
		t.Fatal(err)
	}
	for table, literal := range map[string]string{
		"blob_fts_data":   "kanariblobpayloadxyz",
		"skills_fts_data": "kanariskilldeskxyz",
	} {
		got := ftsShadow(t, dir, table)
		if strings.Contains(got, literal) {
			t.Errorf("SEC7-71: plaintext token %q still in %s after encryptStaged: %q", literal, table, got)
		}
		if !strings.Contains(got, "zt") {
			t.Errorf("SEC7-71: %s holds no keyed token after encryptStaged: %q", table, got)
		}
	}
}

// TestEncryptStagedControl (SEC7-71 negative control): a store that is
// encrypted from the start ends with keyed tokens too, so the assertion above
// distinguishes "converted correctly" from "never indexed".
func TestEncryptStagedControl(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, &Options{Dir: dir, Kind: "cli", Codec: testCodec{id: "dek"}})
	if err != nil {
		t.Fatal(err)
	}
	seedBlobAndSkill(t, s)
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if got := ftsShadow(t, dir, "blob_fts_data"); strings.Contains(got, "kanariblobpayloadxyz") || !strings.Contains(got, "zt") {
		t.Fatalf("control: blob_fts_data = %q", got)
	}
}

// TestUpgradeEncryptedLayoutChecksEveryFTSTable (SEC7-73): a database whose
// events_fts already has the encrypted layout but whose skills_fts does not
// must still be recreated; the old check only looked at events_fts.
func TestUpgradeEncryptedLayoutChecksEveryFTSTable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, &Options{Dir: dir, Kind: "cli", Codec: testCodec{id: "dek"}})
	if err != nil {
		t.Fatal(err)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	db, err := sql.Open("sqlite", filepath.Clean(DBPath(dir)))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE skills_fts`,
		`CREATE VIRTUAL TABLE skills_fts USING fts5(name, description, body, content='', contentless_delete=1, tokenize = "unicode61 remove_diacritics 2")`,
		`UPDATE fts_meta SET layout_version=2, norm_version=1`,
	} {
		if _, xerr := db.ExecContext(ctx, q); xerr != nil {
			t.Fatal(xerr)
		}
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	s2, err := Open(ctx, &Options{Dir: dir, Kind: "cli", Codec: testCodec{id: "dek"}})
	if err != nil {
		t.Fatal(err)
	}
	if cerr := s2.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if ddl := ftsDDL(t, dir, "skills_fts"); !strings.Contains(ddl, "detail=column") {
		t.Fatalf("SEC7-73: skills_fts was not recreated: %s", ddl)
	}
}

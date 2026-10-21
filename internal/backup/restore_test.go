package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/secret"
)

func makeArchive(t *testing.T, data, cfg string) (string, secretKey) {
	t.Helper()
	s, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	blob := []byte("blob-payload")
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	if merr := os.MkdirAll(filepath.Join(data, "blobs", sha[:2]), 0o700); merr != nil {
		t.Fatal(merr)
	}
	if werr := os.WriteFile(filepath.Join(data, "blobs", sha[:2], sha), blob, 0o600); werr != nil {
		t.Fatal(werr)
	}
	ifWer := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("model = \"x\"\n"), 0o600)
	if ifWer != nil {
		t.Fatal(ifWer)
	}
	key := testKey(t)
	res, err := Create(context.Background(), &CreateOptions{DataDir: data, ConfigDir: cfg, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, m, verr := Verify(context.Background(), res.Path, key); verr != nil || len(m.Entries) != 3 {
		t.Fatalf("archive entries = %+v, %v", m, verr)
	}
	return res.Path, key
}

type secretKey = secret.BackupKey

func TestRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	src, srcCfg := filepath.Join(root, "src"), filepath.Join(root, "srccfg")
	if err := os.MkdirAll(srcCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	archive, key := makeArchive(t, src, srcCfg)
	dst, dstCfg := filepath.Join(root, "dst"), filepath.Join(root, "dstcfg")
	if err := os.MkdirAll(dstCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(context.Background(), &RestoreOptions{DataDir: dst, ConfigDir: dstCfg, Archive: archive, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries == 0 || !res.ConfigRestored {
		t.Fatalf("result %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dst, "damnbot.db")); err != nil {
		t.Fatal(err)
	}
	blobs, gerr := filepath.Glob(filepath.Join(dst, "blobs", "*", "*"))
	if gerr != nil || len(blobs) != 1 {
		t.Fatalf("restored blobs = %v, %v", blobs, gerr)
	}
	if b, rerr := os.ReadFile(blobs[0]); rerr != nil || string(b) != "blob-payload" {
		t.Fatalf("blob = %q, %v", b, rerr)
	}
	if b, err := os.ReadFile(filepath.Join(dstCfg, "config.toml")); err != nil || string(b) != "model = \"x\"\n" { //nolint:gosec // test path
		t.Fatalf("config = %q, %v", b, err)
	}
}

func TestRestoreKeepsExistingConfig(t *testing.T) {
	root := t.TempDir()
	src, srcCfg := filepath.Join(root, "src"), filepath.Join(root, "srccfg")
	if err := os.MkdirAll(srcCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	archive, key := makeArchive(t, src, srcCfg)
	dst, dstCfg := filepath.Join(root, "dst"), filepath.Join(root, "dstcfg")
	if err := os.MkdirAll(dstCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstCfg, "config.toml"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(context.Background(), &RestoreOptions{DataDir: dst, ConfigDir: dstCfg, Archive: archive, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if res.ConfigRestored {
		t.Fatal("config overwritten")
	}
	b, rerr := os.ReadFile(filepath.Join(dstCfg, "config.toml")) //nolint:gosec // test path
	if rerr != nil || string(b) != "keep\n" {
		t.Fatalf("config = %q, %v", b, rerr)
	}
}

func TestRestoreDryRunAndWrongKey(t *testing.T) {
	root := t.TempDir()
	src, srcCfg := filepath.Join(root, "src"), filepath.Join(root, "srccfg")
	if err := os.MkdirAll(srcCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	archive, key := makeArchive(t, src, srcCfg)
	dst := filepath.Join(root, "dst")
	if _, err := Restore(context.Background(), &RestoreOptions{DataDir: dst, ConfigDir: filepath.Join(root, "c"), Archive: archive, Key: key, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "damnbot.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote DB: %v", err)
	}
	other := testKey(t)
	if _, err := Restore(context.Background(), &RestoreOptions{DataDir: dst, ConfigDir: filepath.Join(root, "c"), Archive: archive, Key: other}); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong key = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "damnbot.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong key wrote DB: %v", err)
	}
}

func TestManifestRejectsTraversal(t *testing.T) {
	_, err := decodeManifest([]byte(`{"v":1,"entries":[{"path":"../evil","size":1,"mode":384,"sha256":"` + hex64() + `"}]}`))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("traversal = %v", err)
	}
}

func hex64() string { return strings.Repeat("a", 64) }

func TestRestoreDryRunKeepsDiskClean(t *testing.T) {
	root := t.TempDir()
	src, srcCfg := filepath.Join(root, "src"), filepath.Join(root, "srccfg")
	if err := os.MkdirAll(srcCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	archive, key := makeArchive(t, src, srcCfg)
	dst := filepath.Join(root, "dst")
	if _, err := Restore(context.Background(), &RestoreOptions{DataDir: dst, ConfigDir: filepath.Join(root, "c"), Archive: archive, Key: key, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if m, gerr := filepath.Glob(filepath.Join(dst, "run", "restore-*")); gerr != nil || len(m) != 0 {
		t.Fatalf("dry run left staging dirs: %v", m)
	}
	if _, err := os.Stat(filepath.Join(dst, "damnbot.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote DB: %v", err)
	}
}

func TestRestoreFailureLeavesNoStaging(t *testing.T) {
	root := t.TempDir()
	src, srcCfg := filepath.Join(root, "src"), filepath.Join(root, "srccfg")
	if err := os.MkdirAll(srcCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	archive, key := makeArchive(t, src, srcCfg)
	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(filepath.Join(dst, "damnbot.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(context.Background(), &RestoreOptions{DataDir: dst, ConfigDir: filepath.Join(root, "c"), Archive: archive, Key: key}); err == nil {
		t.Skip("install unexpectedly accepted a directory DB")
	}
	if m, gerr := filepath.Glob(filepath.Join(dst, "run", "restore-*")); gerr != nil || len(m) != 0 {
		t.Fatalf("failed restore left staging dirs: %v", m)
	}
}

func TestStagePathRejectsBadBlobs(t *testing.T) {
	hexName := hex64()
	for _, rel := range []string{
		"blobs/aa/" + hexName[:62],
		"blobs/../purge.log",
		"blobs/" + hexName,
		"purge.log",
	} {
		if _, err := stagePath(t.TempDir(), rel); err == nil {
			t.Fatalf("stagePath accepted %q", rel)
		}
	}
	good := "blobs/" + hexName[:2] + "/" + hexName
	if _, err := stagePath(t.TempDir(), good); err != nil {
		t.Fatalf("stagePath rejected %q: %v", good, err)
	}
}

func TestRestoreCorruptLeavesNoStaging(t *testing.T) {
	root := t.TempDir()
	src, srcCfg := filepath.Join(root, "src"), filepath.Join(root, "srccfg")
	if err := os.MkdirAll(srcCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	archive, key := makeArchive(t, src, srcCfg)
	raw, err := os.ReadFile(archive) //nolint:gosec // archive is this test's temp file
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"tampered":  append([]byte(nil), raw...),
		"truncated": raw[:len(raw)/2],
		"trailing":  append(append([]byte(nil), raw...), 'x'),
	}
	cases["tampered"][len(raw)-1] ^= 1
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name+".dmbk")
			if err := os.WriteFile(path, bad, 0o600); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(root, "dst-"+name)
			if _, err := Restore(context.Background(), &RestoreOptions{
				DataDir: dst, ConfigDir: filepath.Join(root, "c-"+name), Archive: path, Key: key,
			}); err == nil {
				t.Fatal("corrupt archive accepted")
			}
			if m, gerr := filepath.Glob(filepath.Join(dst, "run", "restore-*")); gerr != nil || len(m) != 0 {
				t.Fatalf("staging left after %s: %v %v", name, m, gerr)
			}
		})
	}
}

func TestRestorePreflightConfig(t *testing.T) {
	root := t.TempDir()
	src, srcCfg := filepath.Join(root, "src"), filepath.Join(root, "srccfg")
	if err := os.MkdirAll(srcCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	archive, key := makeArchive(t, src, srcCfg)
	badCfg := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(badCfg, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "dst")
	if _, err := Restore(context.Background(), &RestoreOptions{
		DataDir: dst, ConfigDir: badCfg, Archive: archive, Key: key,
	}); err == nil {
		t.Fatal("restore accepted a config file as directory")
	}
	if _, err := os.Stat(filepath.Join(dst, "damnbot.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("DB installed before config preflight: %v", err)
	}
}

func TestRestoreEncryptedBackupToFreshDir(t *testing.T) { //nolint:gocyclo // integration flow
	root := t.TempDir()
	data := filepath.Join(root, "data")
	keys := filepath.Join(root, "keys")
	st, err := secret.New(secret.Options{ConfigDir: keys, BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	codec, cerr := secret.OpenCodec(context.Background(), data, st, true)
	if cerr != nil {
		t.Fatal(cerr)
	}
	db, derr := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli", Codec: codec})
	if derr != nil {
		t.Fatal(derr)
	}
	if _, err := db.CreateSession(context.Background(), ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(context.Background(), "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "encrypted-canary"}); err != nil {
		t.Fatal(err)
	}
	if srcEvs, serr := db.History(context.Background(), "S", 0); serr == nil {
		t.Logf("debug source events=%+v", srcEvs)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "enc.dmbk")
	key := testKey(t)
	if _, err := Create(context.Background(), &CreateOptions{DataDir: data, ConfigDir: filepath.Join(root, "cfg"), Out: archive, Key: key, Codec: codec}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(data); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(context.Background(), &RestoreOptions{
		DataDir: data, ConfigDir: filepath.Join(root, "cfg"), Archive: archive, Key: key,
		KeyStore: st, Encrypt: true,
	}); err != nil {
		t.Fatal(err)
	}
	again, oerr := secret.OpenCodec(context.Background(), data, st, false)
	if oerr != nil {
		t.Fatal(oerr)
	}
	db, oerr = ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli", Codec: again})
	if oerr != nil {
		t.Fatal(oerr)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	evs, herr := db.History(context.Background(), "S", 0)
	if herr != nil || len(evs) != 1 || evs[0].Content != "encrypted-canary" {
		t.Fatalf("history = %+v, %v", evs, herr)
	}
}

func TestRestoreRejectsDifferentStorageKey(t *testing.T) {
	root := t.TempDir()
	data, keys := filepath.Join(root, "data"), filepath.Join(root, "keys")
	st, nerr := secret.New(secret.Options{ConfigDir: keys, BackendPresent: func() bool { return false }})
	if nerr != nil {
		t.Fatal(nerr)
	}
	codec, cerr := secret.OpenCodec(context.Background(), data, st, true)
	if cerr != nil {
		t.Fatal(cerr)
	}
	db, derr := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli", Codec: codec})
	if derr != nil {
		t.Fatal(derr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	archive := filepath.Join(root, "a.dmbk")
	key := testKey(t)
	if _, err := Create(context.Background(), &CreateOptions{DataDir: data, ConfigDir: filepath.Join(root, "cfg"), Out: archive, Key: key, Codec: codec}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "dst")
	other, oerr := secret.OpenCodec(context.Background(), dst, st, true)
	if oerr != nil {
		t.Fatal(oerr)
	}
	_ = other
	if _, err := Restore(context.Background(), &RestoreOptions{
		DataDir: dst, ConfigDir: filepath.Join(root, "cfg"), Archive: archive, Key: key, KeyStore: st, Encrypt: true,
	}); !errors.Is(err, ErrKeySource) {
		t.Fatalf("different storage.key = %v, want ErrKeySource", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "damnbot.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("DB installed despite key conflict: %v", err)
	}
}

func TestRestoreMigratesAllTables(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "src")
	st, err := secret.New(secret.Options{ConfigDir: filepath.Join(root, "keys"), BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	sha := migrationSource(t, data)
	archive := filepath.Join(root, "a.dmbk")
	key := testKey(t)
	if _, cerr := Create(context.Background(), &CreateOptions{DataDir: data, ConfigDir: filepath.Join(root, "cfg"), Out: archive, Key: key}); cerr != nil {
		t.Fatal(cerr)
	}
	dst := filepath.Join(root, "dst")
	if _, rerr := Restore(context.Background(), &RestoreOptions{
		DataDir: dst, ConfigDir: filepath.Join(root, "dstcfg"), Archive: archive, Key: key, KeyStore: st, Encrypt: true,
	}); rerr != nil {
		t.Fatal(rerr)
	}
	verifyMigrated(t, dst, st, sha)
}

func verifyMigrated(t *testing.T, dst string, st *secret.Store, sha string) {
	t.Helper()
	codec, err := secret.OpenCodec(context.Background(), dst, st, false)
	if err != nil {
		t.Fatal(err)
	}
	edb, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dst, Kind: "cli", Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := edb.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	evs, err := edb.History(context.Background(), "S", 0, ctxdb.WithHistoryRaw())
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Content != "canary-event alpha beta" {
		t.Fatalf("history = %+v", evs)
	}
	if blob, berr := edb.ReadBlob(context.Background(), sha); berr != nil || string(blob) != "blob-payload" {
		t.Fatalf("blob = %q, %v", blob, berr)
	}
	hits, err := edb.Search(context.Background(), ctxdb.Query{Text: "canary", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !hasSummaryHit(hits) {
		t.Fatalf("summary search = %+v", hits)
	}
	raw, err := os.ReadFile(filepath.Join(dst, "damnbot.db")) //nolint:gosec // test dir
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"canary-event", "canary-memory", "canary-summary"} {
		if bytes.Contains(raw, []byte(c)) {
			t.Fatalf("plaintext %q in migrated DB", c)
		}
	}
}

func hasSummaryHit(hits []ctxdb.Hit) bool {
	for _, h := range hits {
		if h.Kind == "summary_ref" && strings.Contains(h.Snippet, "canary-summary") {
			return true
		}
	}
	return false
}

func migrationSource(t *testing.T, data string) string {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(context.Background(), ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(context.Background(), "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "canary-event alpha beta"}); err != nil {
		t.Fatal(err)
	}
	blob := []byte("blob-payload")
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(filepath.Join(data, "blobs", sha[:2]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "blobs", sha[:2], sha), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO memory(kind,category,scope,name,content,content_len,origin,created_at) VALUES ('memory','fact','user',NULL,'canary-memory',13,'user',1)`); xerr != nil {
			return fmt.Errorf("memory: %w", xerr)
		}
		var eid int64
		if gerr := tx.Get(`SELECT max(id) FROM events`, nil, &eid); gerr != nil {
			return fmt.Errorf("event id: %w", gerr)
		}
		if _, xerr := tx.Exec(`INSERT INTO summaries(session_id,from_seq,to_seq,content,model,input_tokens,output_tokens,event_id,created_at) VALUES ('S',1,1,'{"text":"canary-summary"}','m',0,0,?,1)`, eid); xerr != nil {
			return fmt.Errorf("summary: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return sha
}

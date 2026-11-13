//go:build !minimal

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/backup"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/secret"
)

func openPortableTestDB(t *testing.T, dir string) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func closePortableTestDB(t *testing.T, db *ctxdb.Store) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Error(err)
	}
}

func seedPortableMemory(t *testing.T, dir, content, status string) {
	t.Helper()
	db := openPortableTestDB(t, dir)
	defer closePortableTestDB(t, db)
	now := time.Now().UnixMilli()
	_, err := memstore.Put(t.Context(), db, &memstore.Row{
		Category: "fact", Scope: "user", Content: content, Origin: "user", CreatedAt: now, ScanResult: "{}",
	}, &memstore.Decision{Status: status, Actor: "user", Reason: "test", CreatedAt: now}, redact.New(redact.Options{}))
	if err != nil {
		t.Fatal(err)
	}
}

func seedPortableSkill(t *testing.T, dir, name, body string, approved bool) {
	t.Helper()
	data := []byte("---\nname: " + name + "\ndescription: seeded\n---\n" + body)
	sum := sha256.Sum256(data)
	db := openPortableTestDB(t, dir)
	defer closePortableTestDB(t, db)
	mid, _, err := skillstore.Quarantine(t.Context(), db, &skillstore.Row{
		Name: name, Description: "seeded", Body: body, SHA256: hex.EncodeToString(sum[:]),
		Path: filepath.Join(dir, "skills", name, "SKILL.md"), Source: "import",
	}, "{}", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if approved {
		if err := skillstore.Approve(t.Context(), db, mid, 0); err != nil {
			t.Fatal(err)
		}
	}
}

func archiveItem(t *testing.T, m portableManifest, kind portableKind, name string) portableItem {
	t.Helper()
	for i := range m.Items {
		if m.Items[i].Kind == kind && (name == "" || m.Items[i].Name == name) {
			return m.Items[i]
		}
	}
	t.Fatalf("archive has no %s %q: %+v", kind, name, m.Items)
	return portableItem{}
}

func itemIndex(m portableManifest, want *portableItem) int {
	for i := range m.Items {
		if m.Items[i].Name == want.Name && m.Items[i].Kind == want.Kind && m.Items[i].SHA256 == want.SHA256 {
			return i
		}
	}
	return 0
}

func TestArchiveRoundTrip(t *testing.T) { //nolint:gocognit,gocyclo // end-to-end assertion list
	ctx := context.Background()
	src := portableTestPaths(t)
	if err := os.WriteFile(src.ConfigFile(), []byte("model = \"fake/model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePortableSkill(t, src.ConfigDir, "disk", "From disk", "Use the disk skill.")
	seedPortableSkill(t, src.DataDir, "indexed", "Indexed body.", true)
	seedPortableSkill(t, src.DataDir, "pending", "Never exported.", false)
	seedPortableMemory(t, src.DataDir, "approved memory", "approved")
	seedPortableMemory(t, src.DataDir, "pending memory", "scanned")
	key := portableTestKey(t)
	out := filepath.Join(t.TempDir(), "portable.dmbk")
	counts, written, err := exportPortable(ctx, src, out, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Skills != 2 || counts.Memory != 1 || counts.Config != 1 {
		t.Fatalf("export counts = %+v", counts)
	}
	m, blobs, err := openPortableArchive(written, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 4 {
		t.Fatalf("items = %+v", m.Items)
	}
	for _, it := range m.Items {
		if it.Kind == portableSkill && (it.Name == "pending") {
			t.Fatal("unapproved skill entered the archive")
		}
		if it.Kind == portableMemory && strings.Contains(string(blobs[itemIndex(m, &it)]), "pending memory") {
			t.Fatal("unapproved memory entered the archive")
		}
	}

	dst := portableTestPaths(t)
	ic, err := importPortable(ctx, dst, written, keepLocal, false, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ic.Changed != 4 {
		t.Fatalf("import counts = %+v", ic)
	}
	cfg, err := os.ReadFile(dst.ConfigFile())
	if err != nil || string(cfg) != "model = \"fake/model\"\n" {
		t.Fatalf("imported config = %q, %v", cfg, err)
	}
	db := openPortableTestDB(t, dst.DataDir)
	defer closePortableTestDB(t, db)
	rows, err := localPortableSkills(ctx, db, "disk")
	if err != nil || len(rows) != 1 || rows[0].memory == 0 {
		t.Fatalf("imported disk skill = %+v, %v", rows, err)
	}
	if st, serr := skillstore.Status(ctx, db, rows[0].memory); serr != nil || st != "scanned" {
		t.Fatalf("imported skill status = %q, %v", st, serr)
	}
	indexed, err := localPortableSkills(ctx, db, "indexed")
	if err != nil || len(indexed) != 1 || indexed[0].memory == 0 {
		t.Fatalf("imported indexed skill = %+v, %v", indexed, err)
	}
	if pending, perr := localPortableSkills(ctx, db, "pending"); perr != nil || len(pending) != 0 {
		t.Fatalf("unapproved skill leaked: %+v, %v", pending, perr)
	}
	hits, err := memstore.Search(ctx, db, &memstore.Query{Scopes: []string{"user"}, Statuses: []string{"scanned", "approved"}, Limit: -1, Now: time.Now().UnixMilli()})
	if err != nil || len(hits) != 1 || hits[0].Row.Content != "approved memory" {
		t.Fatalf("imported memory = %+v, %v", hits, err)
	}

	again, err := importPortable(ctx, dst, written, keepLocal, false, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed != 0 {
		t.Fatalf("re-import not idempotent: %+v", again)
	}
}

func TestArchiveConflicts(t *testing.T) { //nolint:gocognit,gocyclo // table of three conflict modes
	ctx := context.Background()
	key := portableTestKey(t)
	src := portableTestPaths(t)
	if err := os.WriteFile(src.ConfigFile(), []byte("model = \"remote/model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePortableSkill(t, src.ConfigDir, "conf", "Remote", "remote body")
	seedPortableMemory(t, src.DataDir, "remote memory", "approved")
	out := filepath.Join(t.TempDir(), "conflicts.dmbk")
	if _, _, err := exportPortable(ctx, src, out, key, nil); err != nil {
		t.Fatal(err)
	}
	archive, blobs, err := openPortableArchive(out, key)
	if err != nil {
		t.Fatal(err)
	}
	skillItem := archiveItem(t, archive, portableSkill, "conf")
	_ = blobs
	for _, tc := range []struct {
		mode       conflictMode
		wantConfig string
		wantExtra  string
	}{
		{keepLocal, "local", ""},
		{takeRemote, "remote", ""},
		{bothItems, "local", "remote"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			dst := portableTestPaths(t)
			if err := os.WriteFile(dst.ConfigFile(), []byte("model = \"local/model\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			seedPortableSkill(t, dst.DataDir, "conf", "local body", false) //nolint:contextcheck // t.Context inside the helper
			counts, ierr := importPortable(ctx, dst, out, tc.mode, false, key, nil)
			if ierr != nil {
				t.Fatal(ierr)
			}
			cfg, cerr := os.ReadFile(dst.ConfigFile())
			if cerr != nil {
				t.Fatal(cerr)
			}
			if !strings.Contains(string(cfg), tc.wantConfig+"/model") {
				t.Fatalf("config = %q, want %s", cfg, tc.wantConfig)
			}
			if tc.wantExtra != "" {
				extra, rerr := os.ReadFile(dst.ConfigFile() + ".imported")
				if rerr != nil || !strings.Contains(string(extra), tc.wantExtra+"/model") {
					t.Fatalf("config both = %q, %v", extra, rerr)
				}
			}
			db := openPortableTestDB(t, dst.DataDir) //nolint:contextcheck // t.Context inside the helper
			defer closePortableTestDB(t, db)         //nolint:contextcheck // t.Context inside the helper
			rows, rerr := localPortableSkills(ctx, db, "conf")
			if rerr != nil {
				t.Fatal(rerr)
			}
			hasRemote := false
			for i := range rows {
				if rows[i].sha == skillItem.SHA256 {
					hasRemote = true
				}
			}
			switch tc.mode {
			case keepLocal:
				if hasRemote || counts.Conflicts != 2 {
					t.Fatalf("keep-local imported remote: %+v %+v", rows, counts)
				}
			case takeRemote:
				if !hasRemote || counts.Conflicts != 2 {
					t.Fatalf("take-remote kept local: %+v %+v", rows, counts)
				}
			case bothItems:
				suffixed, serr := localPortableSkills(ctx, db, suffixedPortableName("conf", skillItem.SHA256))
				if serr != nil {
					t.Fatal(serr)
				}
				if hasRemote || len(rows) != 1 || len(suffixed) != 1 {
					t.Fatalf("both lost a version: %+v %+v", rows, suffixed)
				}
			}
		})
	}
}

func TestArchiveRejectsTraversalAndSymlinks(t *testing.T) {
	ctx := context.Background()
	src := portableTestPaths(t)
	key := portableTestKey(t)
	// Path traversal in the manifest is rejected before any write.
	m := portableManifest{Schema: portableSchema, App: "test", CreatedAt: time.Now().Unix()}
	body := []byte("---\nname: evil\ndescription: evil\n---\nbody")
	m.Items = append(m.Items, portableItem{Kind: portableSkill, Name: "../evil", Size: int64(len(body)), SHA256: sha256Hex(body)})
	bad := filepath.Join(t.TempDir(), "traversal.dmbk")
	if err := writeRawPortable(t, bad, key, m, [][]byte{body}); err != nil {
		t.Fatal(err)
	}
	if _, err := importPortable(ctx, portableTestPaths(t), bad, keepLocal, false, key, nil); err == nil {
		t.Fatal("path traversal was accepted")
	}
	// A symlinked skill is never followed on export.
	target := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(src.ConfigDir, "skills", "linked")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, _, err := exportPortable(ctx, src, filepath.Join(t.TempDir(), "symlink.dmbk"), key, nil); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink export = %v, want refusal", err)
	}
}

func TestArchiveRejectsGarbage(t *testing.T) {
	if _, _, err := openPortableArchive("/no/such/archive", portableTestKey(t)); err == nil {
		t.Fatal("missing archive accepted")
	}
	bad := filepath.Join(t.TempDir(), "garbage.dmbk")
	if err := os.WriteFile(bad, []byte("not a DMBK1 archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importPortable(context.Background(), portableTestPaths(t), bad, keepLocal, false, portableTestKey(t), nil); err == nil {
		t.Fatal("garbage archive accepted")
	}
}

func writeRawPortable(t *testing.T, out string, key secret.BackupKey, m portableManifest, blobs [][]byte) error {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	sum := sha256.Sum256(raw)
	f, err := os.Create(out) //nolint:gosec // test fixture path
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close archive: %v", cerr)
		}
	})
	w, err := backup.NewWriter(f, key, &backup.WriterOptions{App: "test", ManifestSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(raw))) //nolint:gosec // bounded test manifest
	if _, err := w.Write(lb[:]); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	for _, b := range blobs {
		if _, err := w.Write(b); err != nil {
			return fmt.Errorf("write item: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close writer: %w", err)
	}
	return nil
}

func TestArchiveImportRejectsSecret(t *testing.T) {
	ctx := context.Background()
	key := portableTestKey(t)
	canary := "ghp_" + strings.Repeat("A", 36)
	m := portableManifest{Schema: portableSchema, App: "test", CreatedAt: time.Now().Unix()}
	cfg := []byte("api_key = \"" + canary + "\"\n")
	m.Items = append(m.Items, portableItem{Kind: portableConfig, Name: portableConfigName, Size: int64(len(cfg)), SHA256: sha256Hex(cfg)})
	bad := filepath.Join(t.TempDir(), "tampered.dmbk")
	if err := writeRawPortable(t, bad, key, m, [][]byte{cfg}); err != nil {
		t.Fatal(err)
	}
	if _, err := importPortable(ctx, portableTestPaths(t), bad, keepLocal, false, key, nil); err == nil ||
		!strings.Contains(err.Error(), "plaintext secrets") {
		t.Fatalf("tampered config import = %v, want refusal", err)
	}
}

func TestArchiveCRLFIdempotent(t *testing.T) {
	ctx := context.Background()
	src := portableTestPaths(t)
	if err := os.WriteFile(src.ConfigFile(), []byte("[ui]\r\nreply_language = \"auto\"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePortableSkill(t, src.ConfigDir, "crlf", "CRLF skill", "line one\r\nline two")
	seedPortableMemory(t, src.DataDir, "memory one\r\nmemory two", "approved")
	key := portableTestKey(t)
	out := filepath.Join(t.TempDir(), "crlf.dmbk")
	if _, _, err := exportPortable(ctx, src, out, key, nil); err != nil {
		t.Fatal(err)
	}
	dst := portableTestPaths(t)
	first, err := importPortable(ctx, dst, out, keepLocal, false, key, nil)
	if err != nil || first.Changed != 3 {
		t.Fatalf("first import = %+v, %v", first, err)
	}
	second, err := importPortable(ctx, dst, out, keepLocal, false, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed != 0 {
		t.Fatalf("CRLF re-import not idempotent: %+v", second)
	}
	cfg, err := os.ReadFile(dst.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cfg, []byte("\r\n")) {
		t.Fatalf("imported config was not normalized to LF: %q", cfg)
	}
}

func TestArchiveSkillSemanticIdempotent(t *testing.T) {
	ctx := context.Background()
	src := portableTestPaths(t)
	writePortableSkill(t, src.ConfigDir, "sem", "seeded", "line one\nline two")
	key := portableTestKey(t)
	out := filepath.Join(t.TempDir(), "sem.dmbk")
	if _, _, err := exportPortable(ctx, src, out, key, nil); err != nil {
		t.Fatal(err)
	}
	dst := portableTestPaths(t)
	seedPortableSkill(t, dst.DataDir, "sem", "line one\r\nline two", false)
	counts, err := importPortable(ctx, dst, out, keepLocal, false, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Changed != 0 || len(counts.ChangedItems) != 0 {
		t.Fatalf("semantically identical CRLF skill not idempotent: %+v", counts)
	}
}

func TestArchiveConfigSemanticIdempotent(t *testing.T) {
	ctx := context.Background()
	src := portableTestPaths(t)
	if err := os.WriteFile(src.ConfigFile(), []byte("[ui]\nreply_language = \"auto\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := portableTestKey(t)
	out := filepath.Join(t.TempDir(), "config-eq.dmbk")
	if _, _, err := exportPortable(ctx, src, out, key, nil); err != nil {
		t.Fatal(err)
	}
	dst := portableTestPaths(t)
	if err := os.WriteFile(dst.ConfigFile(), []byte("# local comment\n[ui]\nreply_language = \"auto\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	counts, err := importPortable(ctx, dst, out, keepLocal, false, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Changed != 0 {
		t.Fatalf("semantically equal config not idempotent: %+v", counts)
	}
}

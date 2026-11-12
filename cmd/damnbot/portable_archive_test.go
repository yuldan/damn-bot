//go:build !minimal

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/secret"
)

func portableTestKey(t *testing.T) secret.BackupKey {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := secret.New(secret.Options{ConfigDir: dir, BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.Backup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	return key
}

func portableTestPaths(t *testing.T) config.Paths {
	t.Helper()
	root := t.TempDir()
	p := config.Paths{ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data")}
	for _, d := range []string{p.ConfigDir, p.DataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func writePortableSkill(t *testing.T, cfgDir, name, desc, body string) {
	t.Helper()
	dir := filepath.Join(cfgDir, "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveNoSecret(t *testing.T) {
	ctx := context.Background()
	src := portableTestPaths(t)
	canary := "ghp_" + strings.Repeat("A", 36)
	writePortableSkill(t, src.ConfigDir, "leaky", "Leaky skill", "token "+canary)
	key := portableTestKey(t)
	out := filepath.Join(t.TempDir(), "no-secret.dmbk")
	if _, _, err := exportPortable(ctx, src, out, key, nil); err != nil {
		t.Fatal(err)
	}
	_, blobs, err := openPortableArchive(out, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blobs {
		if bytes.Contains(b, []byte(canary)) || bytes.Contains(b, []byte("ghp_")) {
			t.Fatalf("secret reached the archive payload: %q", b)
		}
		if strings.Contains(string(b), "Leaky skill") && !strings.Contains(string(b), redact.SentinelPrefix) {
			t.Fatalf("skill text was not redacted: %q", b)
		}
	}
	if err := os.WriteFile(src.ConfigFile(), []byte("api_key = \""+canary+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exportPortable(ctx, src, filepath.Join(t.TempDir(), "bad.dmbk"), key, nil); err == nil ||
		!strings.Contains(err.Error(), "plaintext secrets") {
		t.Fatalf("config secret export = %v, want refusal", err)
	}
}

func TestRedaction(t *testing.T) {
	ctx := context.Background()
	src := portableTestPaths(t)
	secrets := []string{
		"sk-" + strings.Repeat("a", 24),
		"ghp_" + strings.Repeat("B", 36),
		"AKIA" + strings.Repeat("C", 16),
		"xoxb-" + strings.Repeat("d", 12),
		"Authorization: Bearer " + strings.Repeat("e", 20),
		"-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat("Zm9v", 8) + "\n-----END RSA PRIVATE KEY-----",
	}
	writePortableSkill(t, src.ConfigDir, "redacted", "Redaction corpus", strings.Join(secrets, "\n"))
	key := portableTestKey(t)
	out := filepath.Join(t.TempDir(), "redaction.dmbk")
	if _, _, err := exportPortable(ctx, src, out, key, nil); err != nil {
		t.Fatal(err)
	}
	m, blobs, err := openPortableArchive(out, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 1 {
		t.Fatalf("items = %+v", m.Items)
	}
	text := string(blobs[0])
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Fatalf("archive payload still contains %q", s)
		}
	}
	if !strings.Contains(text, redact.SentinelPrefix) {
		t.Fatal("archive payload has no redaction sentinel")
	}
}

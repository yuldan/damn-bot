//go:build !minimal

package main

import (
	"archive/zip"
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/doctor"
)

// zipFiles opens an in-memory bundle.
func zipFiles(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(rc)
		if cerr := rc.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		if err != nil {
			t.Fatal(err)
		}
		if perm := f.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o", f.Name, perm)
		}
		out[f.Name] = raw
	}
	return out
}

// TestF8DoctorBundle: the local zip is 0600 and redacted, log lines are
// allowlisted, IDs are HMACs, and the file is written atomically 0600.
func TestF8DoctorBundle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := `config_version = 1
[provider]
  model = "gpt-5"
  api_key = "sk-proj-CANARYCONFIG000000000000"
[gateway.users]
  owner = { name = "Alice CANARYNAME", chat_id = 12345 }
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logs := strings.Join([]string{
		`{"time":"2026-01-01T00:00:00Z","level":"INFO","msg":"turn started","text":"CANARYMESSAGE","chat_id":987654321}`,
		`{"time":"2026-01-01T00:00:01Z","level":"INFO","msg":"free text CANARYMESSAGE","user_id":12345}`,
		`plain text CANARYMESSAGE`,
		`{"time":"2026-01-01T00:00:02Z","level":"WARN","msg":"retry","count":3}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(logDir, "serve.log"), []byte(logs), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := doctor.Report{Checks: []doctor.Check{{ID: "DOC-001", Name: "config", Status: "ok", Message: "sk-proj-CANARYDOCTOR0000000000"}}}
	data, err := buildDoctorBundle(cfgPath, dir, &rep)
	if err != nil {
		t.Fatal(err)
	}
	files := zipFiles(t, data)
	for _, name := range []string{"version.txt", "doctor.json", "config.redacted.toml", "logs/serve.log"} {
		if _, ok := files[name]; !ok {
			t.Errorf("bundle lacks %s (%v)", name, keysOf(files))
		}
	}
	text := string(data)
	for _, bad := range []string{"CANARYMESSAGE", "CANARYNAME", "CANARYCONFIG", "CANARYDOCTOR", "987654321"} {
		if strings.Contains(text, bad) {
			t.Errorf("bundle leaks %q", bad)
		}
	}
	if !strings.Contains(string(files["logs/serve.log"]), `"count":3`) {
		t.Errorf("safe log field missing: %s", files["logs/serve.log"])
	}
	if strings.Contains(string(files["logs/serve.log"]), "12345") {
		t.Errorf("raw platform id leaked: %s", files["logs/serve.log"])
	}

	out := filepath.Join(dir, "bundle.zip")
	if werr := writeBundleFile(out, data); werr != nil {
		t.Fatal(werr)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode = %o", info.Mode().Perm())
	}
}

// TestF8BundleNoSecret: no pastebin host exists in the tree, and the upload
// envelope opens only with the fixture private key.
func TestF8BundleNoSecret(t *testing.T) {
	t.Parallel()
	checkNoPasteHosts(t)
	checkEnvelope(t)
}

func checkNoPasteHosts(t *testing.T) {
	t.Helper()
	hosts := []string{"paste.rs", "dpaste", "pastebin", "hastebin"}
	err := filepath.Walk("../..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.Contains(path, "/.git/") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".js") && !strings.HasSuffix(path, ".html") {
			return nil
		}
		raw, rerr := os.ReadFile(path) // #nosec G122 G304 -- repo-relative source scan
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		for _, h := range hosts {
			if bytes.Contains(raw, []byte(h)) {
				t.Errorf("%s contains paste host %q", path, h)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkEnvelope(t *testing.T) {
	t.Helper()
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealBundle([]byte("redacted zip"), hex.EncodeToString(priv.PublicKey().Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sealed, []byte(bundleMagic)) || bytes.Contains(sealed, []byte("redacted zip")) {
		t.Fatalf("envelope = %q", sealed[:min(len(sealed), 8)])
	}
	eph, err := curve.NewPublicKey(sealed[len(bundleMagic) : len(bundleMagic)+32])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := priv.ECDH(eph)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(shared)
	aead, err := newBundleAEAD(sum[:])
	if err != nil {
		t.Fatal(err)
	}
	nonceAt := len(bundleMagic) + 32
	plain, err := aead.Open(nil, sealed[nonceAt:nonceAt+aead.NonceSize()], sealed[nonceAt+aead.NonceSize():], []byte(bundleMagic))
	if err != nil || string(plain) != "redacted zip" {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

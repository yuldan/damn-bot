package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/mcp/catalog"
)

// TestGAPF5_SEC20CLI verifies the operator-facing path: a signed provider/MCP
// manifest is accepted only with the configured key, and a tampered or
// unsigned file exits non-zero.
func TestGAPF5_SEC20CLI(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	writeRaw := func(m *catalog.Manifest) {
		b, merr := json.Marshal(m)
		if merr != nil {
			t.Fatal(merr)
		}
		if werr := os.WriteFile(path, b, 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	write := func(m *catalog.Manifest, sign bool) {
		if sign {
			raw, serr := m.SigningBytes()
			if serr != nil {
				t.Fatal(serr)
			}
			m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw))
		} else {
			m.Signature = ""
		}
		writeRaw(m)
	}
	m := catalog.Manifest{
		Kind: catalog.KindMCP, Name: "demo", Version: "1.0", Serial: 2,
		Entries: []catalog.Entry{{
			ID: "server", Commit: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64),
		}},
	}
	write(&m, true)
	var stdout, stderr bytes.Buffer
	if code := mcpCatalog([]string{path, "--key", base64.StdEncoding.EncodeToString(pub), "--last-serial", "1"}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "catalog mcp/demo") {
		t.Fatalf("valid = %d %q %q", code, stdout.String(), stderr.String())
	}
	write(&m, false)
	stdout.Reset()
	stderr.Reset()
	if code := mcpCatalog([]string{path, "--key", base64.StdEncoding.EncodeToString(pub)}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "unsigned") {
		t.Fatalf("unsigned = %d %q", code, stderr.String())
	}
	tampered := m
	raw, serr := tampered.SigningBytes()
	if serr != nil {
		t.Fatal(serr)
	}
	tampered.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw))
	tampered.Name = "evil"
	writeRaw(&tampered)
	stdout.Reset()
	stderr.Reset()
	if code := mcpCatalog([]string{path, "--key", base64.StdEncoding.EncodeToString(pub)}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "signature") {
		t.Fatalf("tamper = %d %q", code, stderr.String())
	}
}

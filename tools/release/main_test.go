package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testPassphrase dan testMaterial hanya untuk uji; kunci produksi tidak pernah
// ada di repo. Pasangan ini dibentuk ulang di memori setiap test.
const testPassphrase = "kuda-baterai-paku-2026"

func testMaterial() []byte { return bytes.Repeat([]byte{0x5a}, ed25519.SeedSize) }

func keypair(t testing.TB) (ed25519.PublicKey, string) {
	t.Helper()
	material := testMaterial()
	seed, err := deriveSeed(material, testPassphrase)
	if err != nil {
		t.Fatalf("deriveSeed: %v", err)
	}
	pub, ok := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("public key bukan ed25519.PublicKey")
	}
	return pub, base64.StdEncoding.EncodeToString(material)
}

// writeArtifacts membuat artefak dan SHA256SUMS yang sah untuk daftar nama.
func writeArtifacts(t testing.TB, dir string, names ...string) {
	t.Helper()
	var b strings.Builder
	for _, name := range names {
		content := []byte("artifact:" + name + "\n")
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatalf("tulis %s: %v", name, err)
		}
		sum := sha256.Sum256(content)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	if err := os.WriteFile(filepath.Join(dir, sumsName), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("tulis %s: %v", sumsName, err)
	}
}

func signTest(t testing.TB, dir, material string) {
	t.Helper()
	t.Setenv(keyEnv, material)
	t.Setenv(passEnv, testPassphrase)
	var out bytes.Buffer
	if code := run([]string{"sign", filepath.Join(dir, sumsName)}, strings.NewReader(""), &out, &out); code != 0 {
		t.Fatalf("sign exit %d: %s", code, out.String())
	}
}

func verifyCode(t testing.TB, dir string, pub ed25519.PublicKey) (int, string) {
	t.Helper()
	t.Setenv(pubEnv, base64.StdEncoding.EncodeToString(pub))
	var out bytes.Buffer
	code := run([]string{"verify", "--dir", dir}, strings.NewReader(""), &out, &out)
	return code, out.String()
}

// setupRelease menghasilkan direktori rilis bertanda tangan dengan dua artefak.
func setupRelease(t testing.TB) (dir string, pub ed25519.PublicKey) {
	t.Helper()
	dir = t.TempDir()
	pub, material := keypair(t)
	writeArtifacts(t, dir, "damnbot_test_linux_amd64", "damnbot_test_windows_amd64.exe")
	signTest(t, dir, material)
	return dir, pub
}

func TestSignVerifyRoundTrip(t *testing.T) {
	dir, pub := setupRelease(t)
	code, out := verifyCode(t, dir, pub)
	if code != 0 {
		t.Fatalf("verify artefak sah exit %d: %s", code, out)
	}
	if !strings.Contains(out, "verified 2 artefak") {
		t.Fatalf("verify tidak melaporkan 2 artefak: %q", out)
	}
}

func TestVerifyRejectsTamperedArtifact(t *testing.T) {
	dir, pub := setupRelease(t)
	if err := os.WriteFile(filepath.Join(dir, "damnbot_test_linux_amd64"), []byte("ubah"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := verifyCode(t, dir, pub)
	if code != 1 {
		t.Fatalf("satu byte diubah: exit %d, ingin 1: %s", code, out)
	}
	if !strings.Contains(out, "sha256 tidak cocok") {
		t.Fatalf("alasan kegagalan bukan checksum: %q", out)
	}
}

func TestVerifyRejectsTamperedSums(t *testing.T) {
	dir, pub := setupRelease(t)
	sumsPath := filepath.Join(dir, sumsName)
	data, err := os.ReadFile(sumsPath)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0x01 // byte checksum pertama berubah: tanda tangan tidak lagi sah
	if err := os.WriteFile(sumsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := verifyCode(t, dir, pub)
	if code != 1 || !strings.Contains(out, "tanda tangan") {
		t.Fatalf("SHA256SUMS diubah: exit %d, keluaran %q; ingin gagal tanda tangan", code, out)
	}
}

func TestSignRejectsMismatchedManifest(t *testing.T) {
	dir := t.TempDir()
	writeArtifacts(t, dir, "damnbot_test_linux_amd64")
	sumsPath := filepath.Join(dir, sumsName)
	data, err := os.ReadFile(sumsPath)
	if err != nil {
		t.Fatal(err)
	}
	// Checksum tetap berformat sah tetapi tidak cocok dengan artefak: sign harus menolak
	// menandatangani manifes yang tidak benar (tidak ada tanda tangan yang dibuat).
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	lines[0] = strings.Repeat("0", 64) + lines[0][64:]
	if err := os.WriteFile(sumsPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, material := keypair(t)
	t.Setenv(keyEnv, material)
	t.Setenv(passEnv, testPassphrase)
	var out bytes.Buffer
	code := run([]string{"sign", sumsPath}, strings.NewReader(""), &out, &out)
	if code != 1 || !strings.Contains(out.String(), "SIGN GAGAL") {
		t.Fatalf("sign manifes salah: exit %d, keluaran %q; ingin gagal", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, sigName)); !os.IsNotExist(err) {
		t.Fatalf("sign manifes salah tetap menulis %s", sigName)
	}
}

func TestVerifyRejectsMissingArtifact(t *testing.T) {
	dir, pub := setupRelease(t)
	if err := os.Remove(filepath.Join(dir, "damnbot_test_linux_amd64")); err != nil {
		t.Fatal(err)
	}
	code, out := verifyCode(t, dir, pub)
	if code != 1 || !strings.Contains(out, "VERIFY GAGAL") {
		t.Fatalf("artefak hilang: exit %d, keluaran %q", code, out)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	dir, _ := setupRelease(t)
	otherSeed, err := deriveSeed(testMaterial(), "passphrase-lain")
	if err != nil {
		t.Fatal(err)
	}
	wrong, ok := ed25519.NewKeyFromSeed(otherSeed).Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("public key bukan ed25519.PublicKey")
	}
	code, out := verifyCode(t, dir, wrong)
	if code != 1 || !strings.Contains(out, "tanda tangan") {
		t.Fatalf("kunci salah: exit %d, keluaran %q; ingin gagal tanda tangan", code, out)
	}
}

func TestSignRequiresBothSecrets(t *testing.T) {
	dir := t.TempDir()
	writeArtifacts(t, dir, "damnbot_test_linux_amd64")
	_, material := keypair(t)
	cases := []struct {
		name   string
		key    string
		pass   string
		reason string
	}{
		{"tanpa kunci", "", testPassphrase, keyEnv},
		{"tanpa passphrase", material, "", passEnv},
		{"kunci bukan base64", "!!!", testPassphrase, "base64"},
		{"kunci pendek", base64.StdEncoding.EncodeToString([]byte("pendek")), testPassphrase, "byte"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(keyEnv, tc.key)
			t.Setenv(passEnv, tc.pass)
			var out bytes.Buffer
			code := run([]string{"sign", filepath.Join(dir, sumsName)}, strings.NewReader(""), &out, &out)
			if code != 2 {
				t.Fatalf("exit %d, ingin 2: %s", code, out.String())
			}
			if !strings.Contains(out.String(), tc.reason) {
				t.Fatalf("alasan tidak menyebut %q: %q", tc.reason, out.String())
			}
			if _, err := os.Stat(filepath.Join(dir, sigName)); !os.IsNotExist(err) {
				t.Fatalf("sign gagal tetap meninggalkan %s", sigName)
			}
		})
	}
}

func TestVerifyRequiresPublicKey(t *testing.T) {
	dir, _ := setupRelease(t)
	t.Setenv(pubEnv, "")
	var out bytes.Buffer
	code := run([]string{"verify", "--dir", dir}, strings.NewReader(""), &out, &out)
	if code != 2 || !strings.Contains(out.String(), pubEnv) {
		t.Fatalf("tanpa kunci publik: exit %d, keluaran %q; ingin gagal keras", code, out.String())
	}
}

func TestSignRejectsMalformedSums(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sumsName), []byte("bukan checksum\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, material := keypair(t)
	t.Setenv(keyEnv, material)
	t.Setenv(passEnv, testPassphrase)
	var out bytes.Buffer
	code := run([]string{"sign", filepath.Join(dir, sumsName)}, strings.NewReader(""), &out, &out)
	if code != 1 {
		t.Fatalf("SHA256SUMS rusak: exit %d, ingin 1: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, sigName)); !os.IsNotExist(err) {
		t.Fatalf("sign atas SHA256SUMS rusak menulis %s", sigName)
	}
}

func TestParseSumsRejectsMalformed(t *testing.T) {
	validSum := strings.Repeat("ab", sha256.Size)
	cases := map[string]string{
		"kosong":           "",
		"tanpa newline":    validSum + "  a",
		"hash pendek":      "abc  a\n",
		"pemisah biner":    validSum + " *a\n",
		"path traversal":   validSum + "  ../a\n",
		"absolute":         validSum + "  /etc/passwd\n",
		"nama kosong":      validSum + "  \n",
		"nama berspasi":    validSum + "  a b\n",
		"checksum sendiri": validSum + "  SHA256SUMS\n",
		"tanda tangan":     validSum + "  SHA256SUMS.sig\n",
		"ganda":            validSum + "  a\n" + validSum + "  a\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSums([]byte(input)); err == nil {
				t.Fatalf("parseSums menerima %q", input)
			}
		})
	}
}

func TestParseSignatureRejectsMalformed(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	cases := map[string]string{
		"kosong":        "",
		"header salah":  "damnbot-signature/v0 ed25519\n" + valid + "\n",
		"base64 salah":  sigHeader + "\n!!!\n",
		"panjang salah": sigHeader + "\n" + base64.StdEncoding.EncodeToString([]byte("pendek")) + "\n",
		"baris ekstra":  sigHeader + "\n" + valid + "\n" + valid + "\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSignature([]byte(input)); err == nil {
				t.Fatalf("parseSignature menerima %q", input)
			}
		})
	}
	if sig, err := parseSignature([]byte(sigHeader + "\n" + valid + "\n")); err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf("parseSignature menolak bentuk sah: %v", err)
	}
}

func TestDeriveSeedBindsBothSecrets(t *testing.T) {
	material := testMaterial()
	a, err := deriveSeed(material, "satu")
	if err != nil {
		t.Fatal(err)
	}
	b, err := deriveSeed(material, "satu")
	if err != nil {
		t.Fatal(err)
	}
	c, err := deriveSeed(material, "dua")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("seed tidak deterministik untuk masukan yang sama")
	}
	if bytes.Equal(a, c) {
		t.Fatal("passphrase tidak memengaruhi seed")
	}
	if _, err := deriveSeed(material, ""); err == nil {
		t.Fatal("deriveSeed menerima passphrase kosong")
	}
	if _, err := deriveSeed(material[:8], "x"); err == nil {
		t.Fatal("deriveSeed menerima material pendek")
	}
}

func TestKeygenProducesMatchingPublicKey(t *testing.T) {
	t.Setenv(passEnv, testPassphrase)
	var out bytes.Buffer
	if code := run([]string{"keygen"}, strings.NewReader(""), &out, &out); code != 0 {
		t.Fatalf("keygen exit %d: %s", code, out.String())
	}
	var materialText, pubText string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		switch {
		case strings.HasPrefix(line, "material "):
			materialText = strings.TrimPrefix(line, "material ")
		case strings.HasPrefix(line, "pubkey "):
			pubText = strings.TrimPrefix(line, "pubkey ")
		}
	}
	material, err := base64.StdEncoding.Strict().DecodeString(materialText)
	if err != nil {
		t.Fatalf("material bukan base64: %v", err)
	}
	seed, err := deriveSeed(material, testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	want, ok := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("public key bukan ed25519.PublicKey")
	}
	if got := base64.StdEncoding.EncodeToString(want); got != pubText {
		t.Fatalf("pubkey keygen %q != turunan %q", pubText, got)
	}
}

func TestKeygenRequiresPassphrase(t *testing.T) {
	t.Setenv(passEnv, "")
	var out bytes.Buffer
	if code := run([]string{"keygen"}, strings.NewReader(""), &out, &out); code != 2 {
		t.Fatalf("keygen tanpa passphrase exit %d, ingin 2: %s", code, out.String())
	}
}

const versionMRaw = "/tmp/satu/damnbot: go1.26.10\n" +
	"\tpath\tgithub.com/damnbot/damnbot/cmd/damnbot\n" +
	"\tmod\tgithub.com/damnbot/damnbot\t(devel)\n" +
	"\tdep\tz.example/z\tv1.0.0\n" +
	"\tdep\ta.example/a\tv2.0.0\n" +
	"\tbuild\t-tags=goolm\n" +
	"\tbuild\tCGO_ENABLED=0\n"

func sbomOutput(t testing.TB, raw string) string {
	t.Helper()
	var out bytes.Buffer
	code := run([]string{"sbom", "--artifact", "damnbot_test_linux_amd64", "--sha256", strings.Repeat("ab", sha256.Size)},
		strings.NewReader(raw), &out, &out)
	if code != 0 {
		t.Fatalf("sbom exit %d: %s", code, out.String())
	}
	return out.String()
}

func TestSBOMStableAcrossBinaryPathsAndOrder(t *testing.T) {
	reordered := "/home/dua/damnbot: go1.26.10\n" +
		"\tbuild\tCGO_ENABLED=0\n" +
		"\tdep\ta.example/a\tv2.0.0\n" +
		"\tmod\tgithub.com/damnbot/damnbot\t(devel)\n" +
		"\tbuild\t-tags=goolm\n" +
		"\tpath\tgithub.com/damnbot/damnbot/cmd/damnbot\n" +
		"\tdep\tz.example/z\tv1.0.0\n"
	one, two := sbomOutput(t, versionMRaw), sbomOutput(t, reordered)
	if one != two {
		t.Fatalf("SBOM tidak stabil:\n--- satu ---\n%s--- dua ---\n%s", one, two)
	}
	for _, want := range []string{
		"sbom-format " + sbomFormat,
		"artifact damnbot_test_linux_amd64",
		"sha256 " + strings.Repeat("ab", sha256.Size),
		"go go1.26.10",
		"path\tgithub.com/damnbot/damnbot/cmd/damnbot",
		"mod\tgithub.com/damnbot/damnbot\t(devel)",
	} {
		if !strings.Contains(one, want) {
			t.Fatalf("SBOM tidak memuat %q:\n%s", want, one)
		}
	}
	if strings.Index(one, "dep\ta.example/a") > strings.Index(one, "dep\tz.example/z") {
		t.Fatalf("dep tidak terurut:\n%s", one)
	}
}

func TestSBOMRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"kosong":      "",
		"tanpa tab":   "/tmp/x: go1.26.10\npath\tgithub.com/damnbot/damnbot\n",
		"tanpa path":  "/tmp/x: go1.26.10\n\tmod\tgithub.com/damnbot/damnbot\t(devel)\n",
		"tanpa versi": "/tmp/x\n\tpath\tp\n\tmod\tm\n",
		"path ganda":  "/tmp/x: go1.26.10\n\tpath\ta\n\tpath\tb\n\tmod\tm\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			code := run([]string{"sbom", "--artifact", "damnbot_test", "--sha256", strings.Repeat("ab", sha256.Size)},
				strings.NewReader(raw), &out, &out)
			if code == 0 {
				t.Fatalf("sbom menerima masukan %q", raw)
			}
		})
	}
	// Argumen berbahaya ditolak sebelum membaca stdin.
	for _, args := range [][]string{
		{"sbom", "--artifact", "../x", "--sha256", strings.Repeat("ab", sha256.Size)},
		{"sbom", "--artifact", "ok", "--sha256", "bukanhex"},
		{"sbom", "--artifact", "ok", "--sha256", strings.Repeat("ab", 4)},
	} {
		var out bytes.Buffer
		if code := run(args, strings.NewReader(versionMRaw), &out, &out); code != 2 {
			t.Fatalf("%v: exit %d, ingin 2: %s", args, code, out.String())
		}
	}
}

// Command release adalah helper rilis Damn-Bot (CROSS-CUTTING §1.4/§1.5,
// ROADMAP F3): tanda tangan ed25519 atas SHA256SUMS, verifikasi fail-closed
// tanda tangan + checksum, dan SBOM stabil dari keluaran `go version -m`.
//
// Hanya stdlib (crypto/ed25519, crypto/hkdf, crypto/sha256); tidak ada
// dependency baru dan tidak ada kunci yang ditulis ke disk. Material kunci
// dibaca dari environment (DAMNBOT_RELEASE_KEY + DAMNBOT_RELEASE_PASSPHRASE)
// dan dipakai di memori saja.
//
// Pemakaian (dipanggil oleh scripts/sign-release.sh dan scripts/verify-release.sh):
//
//	release keygen
//	release sign <SHA256SUMS>
//	release verify [--dir DIR] [--pubkey BASE64]
//	go version -m BIN | release sbom --artifact NAME --sha256 HEX
package main

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	keyEnv     = "DAMNBOT_RELEASE_KEY"
	passEnv    = "DAMNBOT_RELEASE_PASSPHRASE"
	pubEnv     = "DAMNBOT_RELEASE_PUBKEY"
	hkdfInfo   = "damnbot-release-ed25519-v1"
	sigHeader  = "damnbot-signature/v1 ed25519"
	sumsName   = "SHA256SUMS"
	sigName    = "SHA256SUMS.sig"
	sbomFormat = "damnbot/go-version-m/v1"

	maxSumsSize = 1 << 20
	maxInfoSize = 4 << 20
	maxNameLen  = 200
)

const usageText = `pemakaian: release <perintah> [argumen]

  keygen                                  buat material kunci baru (baca DAMNBOT_RELEASE_PASSPHRASE)
  sign <SHA256SUMS>                       tulis SHA256SUMS.sig (baca KEY + PASSPHRASE)
  verify [--dir DIR] [--pubkey BASE64]    verifikasi SHA256SUMS.sig + seluruh checksum
  sbom --artifact NAME --sha256 HEX       normalisasi ` + "`go version -m`" + ` dari stdin menjadi SBOM stabil
`

// usageError menandai kesalahan pemakaian/konfigurasi (exit 2). Kesalahan
// verifikasi atau IO adalah exit 1; kegagalan apa pun tidak pernah dianggap lolos.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

func exitCode(err error) int {
	var u *usageError
	if errors.As(err, &u) {
		return 2
	}
	return 1
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usageText)
		return 2
	}
	var err error
	switch args[0] {
	case "keygen":
		err = runKeygen(args[1:], stdout)
	case "sign":
		err = runSign(args[1:], stdout)
	case "verify":
		err = runVerify(args[1:], stdout)
	case "sbom":
		err = runSBOM(args[1:], stdin, stdout)
	default:
		err = usagef("perintah %q tidak dikenal", args[0])
	}
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "damnbot-release: %v\n", err)
	return exitCode(err)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // pesan error ditulis oleh run(), bukan paket flag
	return fs
}

// deriveSeed menurunkan seed ed25519 dari material kunci dan passphrase:
// keduanya wajib, sehingga kebocoran salah satu saja tidak cukup untuk menandatangani.
func deriveSeed(material []byte, passphrase string) ([]byte, error) {
	if len(material) != ed25519.SeedSize {
		return nil, usagef("%s harus %d byte setelah base64 (dapat %d)", keyEnv, ed25519.SeedSize, len(material))
	}
	if passphrase == "" {
		return nil, usagef("%s kosong", passEnv)
	}
	seed, err := hkdf.Key(sha256.New, material, []byte(passphrase), hkdfInfo, ed25519.SeedSize)
	if err != nil {
		return nil, fmt.Errorf("HKDF: %w", err)
	}
	return seed, nil
}

func decodeBase64Env(name string, want int) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, usagef("%s tidak diset", name)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil {
		return nil, usagef("%s bukan base64 standar: %v", name, err)
	}
	if len(decoded) != want {
		return nil, usagef("%s harus %d byte, dapat %d", name, want, len(decoded))
	}
	return decoded, nil
}

func privateKeyFromEnv() (ed25519.PrivateKey, error) {
	material, err := decodeBase64Env(keyEnv, ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	seed, err := deriveSeed(material, os.Getenv(passEnv))
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func decodePublicKey(text string) (ed25519.PublicKey, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, usagef("%s tidak diset (atau --pubkey kosong)", pubEnv)
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil {
		return nil, usagef("kunci publik bukan base64 standar: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, usagef("kunci publik harus %d byte, dapat %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

func runKeygen(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return usagef("keygen tidak menerima argumen")
	}
	passphrase := os.Getenv(passEnv)
	if passphrase == "" {
		return usagef("%s wajib diset untuk keygen", passEnv)
	}
	material := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(material); err != nil {
		return fmt.Errorf("crypto/rand: %w", err)
	}
	seed, err := deriveSeed(material, passphrase)
	if err != nil {
		return err
	}
	pub, ok := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("crypto/ed25519 mengembalikan tipe public key tak dikenal")
	}
	_, err = fmt.Fprintf(stdout, "material %s\npubkey %s\n",
		base64.StdEncoding.EncodeToString(material), base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		return fmt.Errorf("tulis material kunci: %w", err)
	}
	return nil
}

func runSign(args []string, stdout io.Writer) (err error) {
	if len(args) != 1 {
		return usagef("pemakaian: release sign <%s>", sumsName)
	}
	key, err := privateKeyFromEnv()
	if err != nil {
		return err
	}
	dir, name := filepath.Dir(args[0]), filepath.Base(args[0])
	if name != sumsName {
		return usagef("berkas yang ditandatangani harus bernama %s", sumsName)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("buka direktori %s: %w", dir, err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("tutup direktori %s: %w", dir, cerr)
		}
	}()
	sums, err := readRootFile(root, name, maxSumsSize)
	if err != nil {
		return err
	}
	entries, err := parseSums(sums)
	if err != nil {
		return err
	}
	// Jangan pernah menandatangani manifes yang tidak cocok dengan artefak.
	for _, entry := range entries {
		if err := verifyEntry(root, entry); err != nil {
			return fmt.Errorf("SIGN GAGAL: %w", err)
		}
	}
	if err := writeRootFile(root, sigName, formatSignature(ed25519.Sign(key, sums))); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "signed %s (%d artefak, ed25519)\n", filepath.Join(dir, name), len(entries))
	if err != nil {
		return fmt.Errorf("tulis hasil: %w", err)
	}
	return nil
}

func formatSignature(sig []byte) []byte {
	return []byte(sigHeader + "\n" + base64.StdEncoding.EncodeToString(sig) + "\n")
}

func runVerify(args []string, stdout io.Writer) error {
	fs := newFlagSet("verify")
	dir := fs.String("dir", ".", "direktori artefak + SHA256SUMS + SHA256SUMS.sig")
	pubText := fs.String("pubkey", os.Getenv(pubEnv), "kunci publik ed25519 (base64)")
	if err := fs.Parse(args); err != nil {
		return usagef("argumen verify: %v", err)
	}
	if fs.NArg() != 0 {
		return usagef("verify tidak menerima argumen posisional")
	}
	pub, err := decodePublicKey(*pubText)
	if err != nil {
		return err
	}
	return verifyDir(*dir, pub, stdout)
}

// verifyDir memverifikasi tanda tangan atas byte SHA256SUMS lebih dulu, baru
// setiap checksum. Tanda tangan yang tidak sah menghentikan verifikasi sebelum
// satu berkas pun dibaca sebagai "sah" (fail-closed).
func verifyDir(dir string, pub ed25519.PublicKey, stdout io.Writer) (err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("buka direktori %s: %w", dir, err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("tutup direktori %s: %w", dir, cerr)
		}
	}()
	sums, err := readRootFile(root, sumsName, maxSumsSize)
	if err != nil {
		return err
	}
	sigText, err := readRootFile(root, sigName, maxSumsSize)
	if err != nil {
		return err
	}
	sig, err := parseSignature(sigText)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, sums, sig) {
		return errors.New("VERIFY GAGAL: tanda tangan SHA256SUMS tidak sah")
	}
	entries, err := parseSums(sums)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := verifyEntry(root, entry); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(stdout, "verified %d artefak (ed25519 sah)\n", len(entries))
	if err != nil {
		return fmt.Errorf("tulis hasil: %w", err)
	}
	return nil
}

func verifyEntry(root *os.Root, entry sumEntry) (err error) {
	f, err := root.Open(entry.name)
	if err != nil {
		return fmt.Errorf("VERIFY GAGAL: buka %s: %w", entry.name, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("tutup %s: %w", entry.name, cerr)
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("VERIFY GAGAL: stat %s: %w", entry.name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("VERIFY GAGAL: %s bukan berkas reguler", entry.name)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("VERIFY GAGAL: baca %s: %w", entry.name, err)
	}
	if subtle.ConstantTimeCompare(h.Sum(nil), entry.sum) != 1 {
		return fmt.Errorf("VERIFY GAGAL: sha256 tidak cocok untuk %s", entry.name)
	}
	return nil
}

type sumEntry struct {
	name string
	sum  []byte
}

func parseSums(data []byte) ([]sumEntry, error) {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return nil, errors.New("SHA256SUMS kosong atau tidak diakhiri newline")
	}
	lines := strings.Split(string(data[:len(data)-1]), "\n")
	entries := make([]sumEntry, 0, len(lines))
	seen := make(map[string]bool, len(lines))
	for i, line := range lines {
		entry, err := parseSumsLine(line)
		if err != nil {
			return nil, fmt.Errorf("SHA256SUMS baris %d: %w", i+1, err)
		}
		if seen[entry.name] {
			return nil, fmt.Errorf("SHA256SUMS baris %d: nama ganda %q", i+1, entry.name)
		}
		seen[entry.name] = true
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseSumsLine(line string) (sumEntry, error) {
	const prefixLen = 2 + 2*sha256.Size // "  " + 64 hex
	if len(line) < prefixLen+1 || line[sha256.Size*2] != ' ' || line[sha256.Size*2+1] != ' ' {
		return sumEntry{}, errors.New("format harus `<sha256>  <nama>`")
	}
	name := line[prefixLen:]
	if !validName(name) {
		return sumEntry{}, fmt.Errorf("nama berkas tidak valid %q", name)
	}
	sum, err := hex.DecodeString(line[:sha256.Size*2])
	if err != nil {
		return sumEntry{}, fmt.Errorf("sha256 bukan hex: %w", err)
	}
	return sumEntry{name: name, sum: sum}, nil
}

// validName menjaga SHA256SUMS tetap merujuk basename di dalam direktori rilis:
// tanpa path, whitespace, kontrol, nama cadangan, atau berkas checksum itu sendiri.
func validName(name string) bool {
	if name == "" || len(name) > maxNameLen || name == "." || name == ".." {
		return false
	}
	if name == sumsName || name == sigName || strings.HasSuffix(name, ".sig") {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func parseSignature(data []byte) ([]byte, error) {
	text := strings.TrimSuffix(string(data), "\n")
	lines := strings.Split(text, "\n")
	if len(lines) != 2 || lines[0] != sigHeader {
		return nil, errors.New("format SHA256SUMS.sig tidak dikenal")
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(lines[1])
	if err != nil {
		return nil, fmt.Errorf("base64 tanda tangan: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("tanda tangan harus %d byte, dapat %d", ed25519.SignatureSize, len(sig))
	}
	return sig, nil
}

func readRootFile(root *os.Root, name string, max int64) (data []byte, err error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("baca %s: %w", name, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("tutup %s: %w", name, cerr)
		}
	}()
	data, err = io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("baca %s: %w", name, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s melebihi %d byte", name, max)
	}
	return data, nil
}

func writeRootFile(root *os.Root, name string, data []byte) error {
	if err := root.WriteFile(name, data, 0o600); err != nil {
		return fmt.Errorf("tulis %s: %w", name, err)
	}
	return nil
}

type buildInfo struct {
	goVersion string
	path      string
	mod       string
	deps      []string
	builds    []string
	others    []string
}

func runSBOM(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("sbom")
	artifact := fs.String("artifact", "", "nama artefak (basename)")
	digest := fs.String("sha256", "", "sha256 artefak (hex)")
	if err := fs.Parse(args); err != nil {
		return usagef("argumen sbom: %v", err)
	}
	if fs.NArg() != 0 {
		return usagef("sbom tidak menerima argumen posisional")
	}
	if !validName(*artifact) {
		return usagef("--artifact tidak valid: %q", *artifact)
	}
	sum, err := parseDigest(*digest)
	if err != nil {
		return err
	}
	raw, err := readLimited(stdin, maxInfoSize)
	if err != nil {
		return err
	}
	info, err := parseVersionM(raw)
	if err != nil {
		return err
	}
	return writeSBOM(stdout, *artifact, sum, info)
}

func parseDigest(text string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(text))
	if err != nil {
		return "", usagef("--sha256 bukan hex: %v", err)
	}
	if len(raw) != sha256.Size {
		return "", usagef("--sha256 harus %d byte hex", sha256.Size)
	}
	return hex.EncodeToString(raw), nil
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("baca masukan: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("masukan melebihi %d byte", max)
	}
	return data, nil
}

// parseVersionM menormalkan `go version -m`: baris path binary (yang berbeda
// antar mesin) dibuang, `dep`/`build` diurutkan, dan hanya field bermakna yang
// dipertahankan sehingga dua build dari commit yang sama menghasilkan SBOM identik.
func parseVersionM(raw []byte) (buildInfo, error) {
	var info buildInfo
	text := strings.TrimSuffix(string(raw), "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return info, errors.New("keluaran `go version -m` kosong")
	}
	sep := strings.LastIndex(lines[0], ": ")
	if sep <= 0 {
		return info, fmt.Errorf("baris pertama `go version -m` tidak dikenal: %q", lines[0])
	}
	info.goVersion = strings.TrimSpace(lines[0][sep+2:])
	if !strings.HasPrefix(info.goVersion, "go") || strings.ContainsAny(info.goVersion, " \t") {
		return info, fmt.Errorf("versi Go tidak dikenal: %q", info.goVersion)
	}
	for i, line := range lines[1:] {
		if !strings.HasPrefix(line, "\t") {
			return info, fmt.Errorf("baris %d bukan format `go version -m`: %q", i+2, line)
		}
		body := line[1:]
		fields := strings.Split(body, "\t")
		if len(fields) < 2 || fields[0] == "" {
			return info, fmt.Errorf("baris %d tanpa nilai: %q", i+2, line)
		}
		switch fields[0] {
		case "path":
			if info.path != "" {
				return info, errors.New("`go version -m` memuat lebih dari satu baris path")
			}
			info.path = body
		case "mod":
			if info.mod != "" {
				return info, errors.New("`go version -m` memuat lebih dari satu baris mod")
			}
			info.mod = body
		case "dep":
			info.deps = append(info.deps, body)
		case "build":
			info.builds = append(info.builds, body)
		default:
			info.others = append(info.others, body)
		}
	}
	if info.path == "" || info.mod == "" {
		return info, errors.New("`go version -m` tanpa baris path/mod")
	}
	slices.Sort(info.deps)
	slices.Sort(info.builds)
	slices.Sort(info.others)
	return info, nil
}

func writeSBOM(w io.Writer, artifact, digest string, info buildInfo) error {
	var b strings.Builder
	fmt.Fprintf(&b, "sbom-format %s\nartifact %s\nsha256 %s\ngo %s\n%s\n%s\n",
		sbomFormat, artifact, digest, info.goVersion, info.path, info.mod)
	for _, group := range [][]string{info.deps, info.builds, info.others} {
		for _, line := range group {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("tulis SBOM: %w", err)
	}
	return nil
}

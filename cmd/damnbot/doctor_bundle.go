//go:build !minimal

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/damnbot/damnbot/internal/doctor"
)

// maintainerBundleKey is the embedded X25519 recipient of `doctor bundle
// --upload`; `--out` writes an encrypted file for manual attachment, so no
// upload host exists (GAP-SEC-17).
const maintainerBundleKey = "d95e3929927e0cd1e66f16740cfc9ed34f557c527f4cef233bf0a39a78eb0355"

const bundleMagic = "DMBX1"

func runDoctorBundle(args []string, stdout, stderr io.Writer) int {
	var out string
	upload := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--upload":
			upload = true
		case args[i] == "--out" && i+1 < len(args):
			i++
			out = args[i]
		case strings.HasPrefix(args[i], "--out="):
			out = strings.TrimPrefix(args[i], "--out=")
		default:
			say(stderr, "doctor bundle: unknown argument %q (redaction cannot be disabled)\n", cleanArg(args[i]))
			return 2
		}
	}
	loaded, ok := doctorState(stderr)
	if !ok {
		return 1
	}
	rep := doctor.Run(context.Background(), &doctor.Options{
		ConfigDir: loaded.paths.ConfigDir, DataDir: loaded.paths.DataDir, ConfigFile: loaded.paths.ConfigFile(),
		ConfigFound: loaded.found, ConfigErr: loaded.cfgErr, ProviderModel: loaded.cfg.Provider.Model,
		ProviderWarnings: doctorProviderFindings(&loaded.cfg),
	})
	if out == "" {
		out = filepath.Join(loaded.paths.DataDir, "doctor-bundle-"+time.Now().UTC().Format("20060102T150405Z")+".zip")
	}
	data, err := buildDoctorBundle(loaded.paths.ConfigFile(), loaded.paths.DataDir, &rep)
	if err != nil {
		return f8CmdFail(stderr, "doctor bundle", err)
	}
	if werr := writeBundleFile(out, data); werr != nil {
		return f8CmdFail(stderr, "doctor bundle", werr)
	}
	say(stdout, "wrote %s (%d bytes, mode 0600, redacted)\n", out, len(data))
	if !upload {
		return 0
	}
	sealed, err := sealBundle(data, maintainerBundleKey)
	if err != nil {
		return f8CmdFail(stderr, "doctor bundle", err)
	}
	if err := writeBundleFile(out+".enc", sealed); err != nil {
		return f8CmdFail(stderr, "doctor bundle", err)
	}
	say(stdout, "wrote %s (%d bytes, X25519 ciphertext); attach manually, no upload performed\n", out+".enc", len(sealed))
	return 0
}

// buildDoctorBundle renders the redacted zip in memory from an allowlist:
// log lines are reduced to safe fields and the config is a curated subset, so
// message bodies and platform ids never enter.
func buildDoctorBundle(configFile, dataDir string, rep *doctor.Report) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, body []byte) error {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o600)
		h.Modified = time.Now()
		w, err := zw.CreateHeader(h)
		if err != nil {
			return fmt.Errorf("doctor bundle: zip header: %w", err)
		}
		if _, err := w.Write(body); err != nil {
			return fmt.Errorf("doctor bundle: zip write: %w", err)
		}
		return nil
	}
	doc, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("doctor bundle: json: %w", err)
	}
	for name, body := range map[string]string{
		"version.txt": version + "\n", "doctor.json": f8Redact(string(doc)) + "\n",
		"config.redacted.toml": redactConfigFile(configFile),
	} {
		if err := add(name, []byte(body)); err != nil {
			return nil, err
		}
	}
	if err := addLogs(add, dataDir); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("doctor bundle: zip: %w", err)
	}
	return buf.Bytes(), nil
}

// redactConfigFile keeps non-identifying scalar lines; secrets, paths, names
// and gateway identity sections are dropped.
func redactConfigFile(path string) string {
	raw, err := os.ReadFile(path) // #nosec G304 -- path comes from doctorState, never user input
	if err != nil {
		return "# config unavailable\n"
	}
	var out strings.Builder
	skip := false
	for line := range strings.SplitSeq(string(raw), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			skip = strings.Contains(trim, "gateway.users") || strings.Contains(trim, "gateway.identities")
		}
		if skip || trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		key := trim
		if k, _, ok := strings.Cut(trim, "="); ok {
			key = strings.ToLower(strings.TrimSpace(k))
		}
		if sensitiveConfigKey(key) {
			continue
		}
		out.WriteString(f8Redact(line))
		out.WriteByte('\n')
	}
	return out.String()
}

func sensitiveConfigKey(key string) bool {
	for _, bad := range []string{"name", "username", "user_id", "chat_id", "token", "secret", "password", "key", "cwd", "path", "dir", "sender"} {
		if strings.Contains(key, bad) {
			return true
		}
	}
	return false
}

// addLogs appends the last 2,000 lines of each log, reduced to an allowlist;
// free text is dropped because it can carry a message body.
func addLogs(add func(string, []byte) error, dataDir string) error {
	files, err := filepath.Glob(filepath.Join(dataDir, "logs", "*.log"))
	if err != nil {
		return fmt.Errorf("doctor bundle: logs: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("doctor bundle: salt: %w", err)
	}
	for _, f := range files {
		raw, rerr := os.ReadFile(f) // #nosec G304 -- f is <dataDir>/logs/*.log from Glob
		if rerr != nil {
			continue
		}
		lines := strings.Split(string(raw), "\n")
		if len(lines) > 2000 {
			lines = lines[len(lines)-2000:]
		}
		var out strings.Builder
		for _, line := range lines {
			if clean, ok := cleanLogLine(line, salt); ok {
				out.WriteString(clean)
				out.WriteByte('\n')
			}
		}
		if err := add("logs/"+filepath.Base(f), []byte(f8Redact(out.String()))); err != nil {
			return err
		}
	}
	return nil
}

// cleanLogLine keeps safe structured fields; content keys are dropped and
// platform ids become a per-bundle HMAC prefix.
func cleanLogLine(line string, salt []byte) (string, bool) {
	var row map[string]any
	if json.Unmarshal([]byte(line), &row) != nil {
		return "", false
	}
	keep := map[string]bool{
		"time": true, "level": true, "msg": true, "module": true, "op": true, "error": true,
		"err": true, "code": true, "method": true, "path": true, "status": true,
		"duration": true, "count": true, "sha": true,
	}
	out := make(map[string]any, len(row))
	for k, v := range row {
		lk := strings.ToLower(k)
		switch {
		case lk == "chat_id" || lk == "user_id" || lk == "sender" || lk == "identity":
			out[lk+"_hmac"] = hmacID(salt, fmt.Sprint(v))
		case keep[lk]:
			if s, ok := v.(string); ok && len(s) > 160 {
				continue
			}
			out[lk] = v
		}
	}
	if len(out) == 0 {
		return "", false
	}
	b, err := json.Marshal(out)
	return string(b), err == nil
}

func hmacID(salt []byte, id string) string {
	m := hmac.New(sha256.New, salt)
	m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil))[:12]
}

// writeBundleFile writes 0600 from the first byte through a temp file and an
// atomic rename.
func writeBundleFile(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if merr := os.MkdirAll(dir, 0o700); merr != nil { // #nosec G703 -- --out is the operator's chosen path
		return fmt.Errorf("doctor bundle: dir: %w", merr)
	}
	tmp, terr := os.CreateTemp(dir, ".bundle-*")
	if terr != nil {
		return fmt.Errorf("doctor bundle: temp: %w", terr)
	}
	name := tmp.Name()
	defer func() {
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err == nil { // #nosec G703 -- temp path from CreateTemp
			err = fmt.Errorf("doctor bundle: cleanup: %w", rerr)
		}
	}()
	if cerr := tmp.Chmod(0o600); cerr != nil {
		return errors.Join(fmt.Errorf("doctor bundle: mode: %w", cerr), tmp.Close())
	}
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("doctor bundle: write: %w", err)
	}
	if err := os.Rename(name, path); err != nil { // #nosec G703 -- same configured path
		return fmt.Errorf("doctor bundle: rename: %w", err)
	}
	return nil
}

// sealBundle encrypts the zip to the maintainer's X25519 key.
func sealBundle(zipData []byte, pubHex string) ([]byte, error) {
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != 32 {
		return nil, errors.New("doctor bundle: recipient key")
	}
	curve := ecdh.X25519()
	recipient, err := curve.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("doctor bundle: recipient: %w", err)
	}
	eph, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("doctor bundle: ephemeral key: %w", err)
	}
	shared, err := eph.ECDH(recipient)
	if err != nil {
		return nil, fmt.Errorf("doctor bundle: exchange: %w", err)
	}
	sum := sha256.Sum256(shared)
	aead, err := newBundleAEAD(sum[:])
	if err != nil {
		return nil, fmt.Errorf("doctor bundle: cipher: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("doctor bundle: nonce: %w", err)
	}
	out := append([]byte{}, bundleMagic...)
	out = append(out, eph.PublicKey().Bytes()...)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, zipData, []byte(bundleMagic)), nil
}

func newBundleAEAD(key []byte) (cipher.AEAD, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("doctor bundle: cipher: %w", err)
	}
	return aead, nil
}

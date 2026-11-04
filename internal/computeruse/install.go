package computeruse

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrChecksum rejects a source whose SHA-256 does not match the recorded one.
var ErrChecksum = errors.New("computeruse: checksum mismatch")

// MaxInstallBytes bounds one staged driver copy.
const MaxInstallBytes = 64 << 20

// InstallOptions stages the external cua driver under Dir; an empty Source
// inspects an existing install and SHA256 falls back to a `.sha256` sidecar.
type InstallOptions struct {
	Dir    string
	Source string
	SHA256 string
	GOOS   string
}

// InstallResult reports one idempotent install pass.
type InstallResult struct {
	Path      string
	SHA256    string
	Installed bool // a copy or replacement happened
	Verified  bool // a recorded digest was available and checked
}

// Install stages Source as <Dir>/cua/cua-driver[.exe] via a verified temp file; a mismatch leaves no partial file.
func Install(opts InstallOptions) (InstallResult, error) {
	if opts.Dir == "" || !filepath.IsAbs(opts.Dir) {
		return InstallResult{}, fmt.Errorf("%w: data dir must be absolute", ErrInvalid)
	}
	name := "cua-driver"
	if opts.GOOS == "windows" || (opts.GOOS == "" && runtime.GOOS == "windows") {
		name += ".exe"
	}
	res := InstallResult{Path: filepath.Join(opts.Dir, "cua", name)}
	expected, err := installChecksum(opts, res.Path)
	if err != nil {
		return res, err
	}
	res.SHA256 = expected
	if opts.Source == "" {
		return inspectInstall(res, expected)
	}
	sum, err := hashFile(opts.Source)
	if err != nil {
		return res, err
	}
	if expected != "" && !strings.EqualFold(expected, sum) {
		return res, fmt.Errorf("%w: source %s is %s, want %s", ErrChecksum, opts.Source, sum, expected)
	}
	if stored, ok := storedDigest(res.Path); ok && stored == sum {
		res.SHA256, res.Verified = sum, true
		return res, nil
	}
	if err := stageInstall(opts.Source, res.Path, sum); err != nil {
		return res, err
	}
	// The digest sidecar is written after the target so a reader never trusts a
	// checksum for a file that does not exist yet.
	if err := writeChecksum(res.Path, sum); err != nil {
		return res, err
	}
	res.SHA256, res.Installed, res.Verified = sum, true, true
	return res, nil
}

// installChecksum prefers the explicit digest, then the sidecar, then the
// digest recorded next to an existing install.
func installChecksum(opts InstallOptions, target string) (string, error) {
	if opts.SHA256 != "" {
		return parseDigest(opts.SHA256)
	}
	if opts.Source != "" {
		if raw, err := os.ReadFile(opts.Source + ".sha256"); err == nil { // #nosec G304 -- operator-selected source
			return parseDigest(string(raw))
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read checksum: %w", err)
		}
		return "", nil
	}
	raw, ok := storedDigest(target)
	if !ok {
		return "", nil
	}
	return raw, nil
}

// inspectInstall verifies an existing staged driver in place.
func inspectInstall(res InstallResult, expected string) (InstallResult, error) {
	if _, err := os.Stat(res.Path); errors.Is(err, os.ErrNotExist) {
		return res, fmt.Errorf("%w: cua driver is not installed; pass --source", ErrUnavailable)
	} else if err != nil {
		return res, fmt.Errorf("stat %s: %w", res.Path, err)
	}
	sum, err := hashFile(res.Path)
	if err != nil {
		return res, err
	}
	if expected != "" && !strings.EqualFold(expected, sum) {
		return res, fmt.Errorf("%w: installed %s is %s, want %s", ErrChecksum, res.Path, sum, expected)
	}
	res.SHA256, res.Verified = sum, expected != ""
	return res, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-selected source or own install
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxInstallBytes+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if n > MaxInstallBytes {
		return "", fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, path, MaxInstallBytes)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func stageInstall(src, dst, want string) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	in, err := os.Open(src) // #nosec G304 -- operator-selected source
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	tmp, err := os.CreateTemp(dir, ".cua-driver-*")
	if err != nil {
		if cerr := in.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(in, MaxInstallBytes+1))
	if cerr := in.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxInstallBytes {
		err = fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, src, MaxInstallBytes)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = fmt.Errorf("%w: source changed during copy", ErrChecksum)
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755) // #nosec G302 -- the installed driver must be executable
	}
	if err == nil {
		err = replaceFile(tmp.Name(), dst)
	}
	if err != nil {
		return removeTemp(tmp.Name(), err)
	}
	return nil
}

// removeTemp discards a staged file while preserving every failure cause.
func removeTemp(name string, cause error) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(cause, err)
	}
	return cause
}

func writeChecksum(target, sum string) error {
	line := sum + "  " + filepath.Base(target) + "\n"
	if err := os.WriteFile(target+".sha256", []byte(line), 0o600); err != nil {
		return fmt.Errorf("write checksum: %w", err)
	}
	return nil
}

// replaceFile renames tmp over dst; Windows refuses an existing destination,
// so the stale file is removed first there. The caller has already verified tmp.
func replaceFile(tmp, dst string) error {
	if runtime.GOOS == "windows" {
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", dst, err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

func storedDigest(target string) (string, bool) {
	raw, err := os.ReadFile(target + ".sha256") // #nosec G304 -- sidecar written by this package
	if err != nil {
		return "", false
	}
	sum, err := parseDigest(string(raw))
	if err != nil {
		return "", false
	}
	return sum, true
}

func parseDigest(raw string) (string, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return "", fmt.Errorf("%w: empty checksum", ErrInvalid)
	}
	sum := strings.ToLower(fields[0])
	if _, err := hex.DecodeString(sum); len(sum) != 64 || err != nil {
		return "", fmt.Errorf("%w: checksum %q is not SHA-256", ErrInvalid, raw)
	}
	return sum, nil
}

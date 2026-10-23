package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/secret"
)

// RestoreOptions configures one archive restore.
type RestoreOptions struct {
	DataDir   string
	ConfigDir string
	Archive   string
	Key       secret.BackupKey
	DryRun    bool
}

// RestoreResult reports what a restore did or would do.
type RestoreResult struct {
	Entries         int
	MissingBlobs    int
	ConfigRestored  bool
	SecretsRestored bool
	Skipped         []string
}

// Restore verifies the archive into a private staging directory and only then
// installs the DB/blobs under the maintenance barrier. Existing config.toml
// and secrets.enc are never overwritten.
func Restore(ctx context.Context, o *RestoreOptions) (res RestoreResult, err error) {
	if o.DataDir == "" || o.Archive == "" || len(o.Key.Private) != keySize {
		return res, fmt.Errorf("backup: restore options: %w", ErrKey)
	}
	f, r, m, size, err := openRestoreArchive(ctx, o)
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	res.Entries, res.MissingBlobs = len(m.Entries), len(m.MissingBlobs)
	if o.DryRun {
		if verr := verifyPayload(ctx, r, &m); verr != nil {
			return res, verr
		}
		return res, nil
	}
	stage, err := stageArchive(ctx, o, r, &m, size)
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, removeAll(stage)) }()
	if cerr := preflightConfig(o.ConfigDir); cerr != nil {
		return res, cerr
	}
	if _, serr := installIfAbsent(filepath.Join(o.DataDir, "storage.key"), filepath.Join(stage, "storage.key")); serr != nil {
		return res, serr
	}
	if rerr := ctxdb.Restore(ctx, &ctxdb.Options{Dir: o.DataDir, Kind: "cli"}, ctxdb.RestoreSet{
		DB: filepath.Join(stage, "damnbot.db"), Blobs: stage,
	}); rerr != nil {
		return res, fmt.Errorf("backup: install: %w", rerr)
	}
	config, skipped, err := restoreUserFiles(o, stage, m.IncludeSecrets)
	if err != nil {
		return res, err
	}
	res.ConfigRestored, res.SecretsRestored = config, skipped
	if !config {
		res.Skipped = append(res.Skipped, "config.toml")
	}
	if m.IncludeSecrets && !skipped {
		res.Skipped = append(res.Skipped, "secrets.enc")
	}
	return res, nil
}

func openRestoreArchive(ctx context.Context, o *RestoreOptions) (*os.File, *Reader, Manifest, int64, error) {
	fi, err := os.Stat(o.Archive)
	if err != nil {
		return nil, nil, Manifest{}, 0, fmt.Errorf("backup: stat archive: %w", err)
	}
	f, err := os.Open(filepath.Clean(o.Archive))
	if err != nil {
		return nil, nil, Manifest{}, 0, fmt.Errorf("backup: open archive: %w", err)
	}
	h, r, err := NewReader(f, o.Key)
	if err != nil {
		return nil, nil, Manifest{}, 0, errors.Join(err, f.Close())
	}
	m, err := readAndCheckManifest(ctx, r, &h)
	if err != nil {
		return nil, nil, Manifest{}, 0, errors.Join(err, f.Close())
	}
	return f, r, m, fi.Size(), nil
}

func preflightConfig(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("backup: config dir: %w", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("backup: config dir: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("backup: %s is not a directory: %w", dir, ErrPermissions)
	}
	return nil
}

func restoreUserFiles(o *RestoreOptions, stage string, includeSecrets bool) (config, sec bool, err error) {
	config, err = installIfAbsent(filepath.Join(o.ConfigDir, "config.toml"), filepath.Join(stage, "config", "config.toml"))
	if err != nil {
		return false, false, err
	}
	if includeSecrets {
		sec, err = installIfAbsent(filepath.Join(o.ConfigDir, "secrets.enc"), filepath.Join(stage, "config", "secrets.enc"))
	}
	return config, sec, err
}

func stageArchive(ctx context.Context, o *RestoreOptions, r io.Reader, m *Manifest, archiveSize int64) (stage string, err error) {
	if merr := os.MkdirAll(filepath.Join(o.DataDir, "run"), 0o700); merr != nil {
		return "", fmt.Errorf("backup: create run dir: %w", merr)
	}
	stage, err = os.MkdirTemp(filepath.Join(o.DataDir, "run"), "restore-")
	if err != nil {
		return "", fmt.Errorf("backup: staging: %w", err)
	}
	dir := stage
	keep := false
	defer func() {
		if !keep {
			err = errors.Join(err, removeAll(dir))
		}
	}()
	// MkdirTemp creates the staging directory with mode 0700 already.
	for _, e := range m.Entries {
		if archiveSize < e.Size {
			return "", fmt.Errorf("%w: entry %s exceeds archive", ErrCorrupt, e.Path)
		}
		target, terr := stagePath(stage, e.Path)
		if terr != nil {
			return "", terr
		}
		if xerr := extract(ctx, r, target, e); xerr != nil {
			return "", xerr
		}
	}
	var one [1]byte
	if n, rerr := r.Read(one[:]); n != 0 || !errors.Is(rerr, io.EOF) {
		return "", fmt.Errorf("%w: trailing payload", ErrCorrupt)
	}
	keep = true
	return stage, nil
}

func validBlobRel(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 64 || parts[0] != parts[1][:2] {
		return false
	}
	for _, p := range parts {
		if _, err := hex.DecodeString(p); err != nil {
			return false
		}
	}
	return true
}

func verifyPayload(ctx context.Context, r io.Reader, m *Manifest) error {
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		got, err := hashN(r, e.Size)
		if err != nil {
			return err
		}
		if got != e.SHA256 {
			return fmt.Errorf("%w: entry %s hash", ErrCorrupt, e.Path)
		}
	}
	var one [1]byte
	if n, err := r.Read(one[:]); n != 0 || !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing payload", ErrCorrupt)
	}
	return nil
}

func removeAll(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("backup: remove staging: %w", err)
	}
	return nil
}

func stagePath(stage, rel string) (string, error) {
	if !validPath(rel) {
		return "", fmt.Errorf("%w: path %q", ErrCorrupt, rel)
	}
	var sub string
	switch {
	case rel == "damnbot.db":
		sub = "damnbot.db"
	case rel == "config.toml" || rel == "secrets.enc":
		sub = filepath.Join("config", rel)
	case rel == "storage.key":
		sub = "storage.key"
	case strings.HasPrefix(rel, "blobs/"):
		if !validBlobRel(strings.TrimPrefix(rel, "blobs/")) {
			return "", fmt.Errorf("%w: blob path %q", ErrCorrupt, rel)
		}
		sub = filepath.FromSlash(rel)
	default:
		return "", fmt.Errorf("%w: unexpected path %q", ErrCorrupt, rel)
	}
	p := filepath.Join(stage, sub)
	if !strings.HasPrefix(p, filepath.Clean(stage)+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: path escapes staging: %w", ErrCorrupt, ErrCorrupt)
	}
	return p, nil
}

// createExclusive opens target for writing without replacing anything.
// Both staging extraction and config install must never clobber an existing
// file, so the O_EXCL contract lives in one place.
//
//nolint:gosec // G304: targets come from stagePath/installIfAbsent, never from the request
func createExclusive(target string) (*os.File, error) {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("backup: create %s: %w", target, err)
	}
	return f, nil
}

func extract(ctx context.Context, r io.Reader, target string, e Entry) (err error) {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("backup: %w", cerr)
	}
	if merr := os.MkdirAll(filepath.Dir(target), 0o700); merr != nil {
		return fmt.Errorf("backup: mkdir staging: %w", merr)
	}
	f, err := createExclusive(target)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	h := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(f, h), r, e.Size); err != nil {
		return fmt.Errorf("%w: entry %s: %w", ErrTruncated, e.Path, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return fmt.Errorf("%w: entry %s hash", ErrCorrupt, e.Path)
	}
	return nil
}

func installIfAbsent(dst, src string) (ok bool, err error) {
	if _, serr := os.Stat(src); errors.Is(serr, os.ErrNotExist) {
		return false, nil
	} else if serr != nil {
		return false, fmt.Errorf("backup: stat %s: %w", src, serr)
	}
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return false, fmt.Errorf("backup: open %s: %w", src, err)
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	tmp := dst + ".restore"
	if rerr := removeFile(tmp); rerr != nil {
		return false, rerr
	}
	f, err := createExclusive(tmp)
	if err != nil {
		return false, err
	}
	_, cerr := io.Copy(f, in)
	serr := f.Sync()
	xerr := f.Close()
	if err := errors.Join(cerr, serr, xerr); err != nil {
		return false, fmt.Errorf("backup: write %s: %w", tmp, errors.Join(err, removeFile(tmp)))
	}
	if lerr := os.Link(tmp, dst); lerr != nil {
		lerr = errors.Join(lerr, removeFile(tmp))
		if errors.Is(lerr, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("backup: publish %s: %w", dst, lerr)
	}
	if err := os.Remove(tmp); err != nil {
		return true, fmt.Errorf("backup: remove %s: %w", tmp, err)
	}
	return true, nil
}

package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/secret"
)

// CreateOptions describes one encrypted archive.
type CreateOptions struct {
	DataDir        string
	ConfigDir      string
	Out            string
	App            string
	IncludeSecrets bool
	Key            secret.BackupKey
	Now            time.Time
}

// Result is the published archive.
type Result struct {
	Path   string
	Header Header
	Size   int64
}

// Create snapshots the database, copies config/blobs into a DMBK1 payload and
// publishes the archive with 0600 permissions. It never writes plaintext.
func Create(ctx context.Context, o *CreateOptions) (res Result, err error) {
	if o.DataDir == "" || len(o.Key.Private) != keySize {
		return res, fmt.Errorf("backup: create options: %w", ErrKey)
	}
	dir := filepath.Join(o.DataDir, "backups")
	if derr := ensureDir(dir); derr != nil {
		return res, derr
	}
	snap, err := snapshotFile(dir)
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, removeFile(snap)) }()
	live, err := ctxdb.SnapshotTo(ctx, o.DataDir, snap)
	if err != nil {
		return res, fmt.Errorf("backup: snapshot: %w", err)
	}
	m, err := buildManifest(o, snap, live)
	if err != nil {
		return res, err
	}
	raw, err := encodeManifest(m)
	if err != nil {
		return res, err
	}
	return publish(ctx, o, raw, snap)
}

func snapshotFile(dir string) (string, error) {
	f, err := os.CreateTemp(dir, ".snapshot-")
	if err != nil {
		return "", fmt.Errorf("backup: snapshot: %w", err)
	}
	name := f.Name()
	cerr := f.Close()
	rerr := removeFile(name)
	if err := errors.Join(cerr, rerr); err != nil {
		return "", fmt.Errorf("backup: snapshot: %w", err)
	}
	return name, nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("backup: remove %s: %w", path, err)
	}
	return nil
}

func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("backup: create %s: %w", dir, err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("backup: stat %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("backup: %s is not a directory: %w", dir, ErrCorrupt)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("backup: %s mode %04o: %w", dir, fi.Mode().Perm(), ErrPermissions)
	}
	return nil
}

func buildManifest(o *CreateOptions, snap string, live []string) (*Manifest, error) {
	m := &Manifest{Version: 1, CreatedAt: o.Now.UTC().Unix(), App: o.App, IncludeSecrets: o.IncludeSecrets}
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().UTC().Unix()
	}
	if err := appendEntry(m, "damnbot.db", snap, 0o600); err != nil {
		return nil, err
	}
	if err := appendOptional(m, "config.toml", filepath.Join(o.ConfigDir, "config.toml")); err != nil {
		return nil, err
	}
	if err := appendOptional(m, "storage.key", filepath.Join(o.DataDir, "storage.key")); err != nil {
		return nil, err
	}
	if o.IncludeSecrets {
		if err := appendOptional(m, "secrets.enc", filepath.Join(o.ConfigDir, "secrets.enc")); err != nil {
			return nil, err
		}
	}
	if err := addBlobs(m, o.DataDir); err != nil {
		return nil, err
	}
	for _, sha := range live {
		p := filepath.Join(o.DataDir, "blobs", sha[:2], sha)
		if _, serr := os.Stat(p); errors.Is(serr, fs.ErrNotExist) {
			m.MissingBlobs = append(m.MissingBlobs, sha)
		}
	}
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	if err := validateManifest(m); err != nil {
		return nil, err
	}
	return m, nil
}

func addBlobs(m *Manifest, dataDir string) error {
	blobs := filepath.Join(dataDir, "blobs")
	err := filepath.WalkDir(blobs, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if errors.Is(werr, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("backup: walk blobs: %w", werr)
		}
		if d.IsDir() {
			if d.Name() == "tmp" || d.Name() == ".trash" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || len(d.Name()) != 64 {
			return nil
		}
		rel, rerr := filepath.Rel(dataDir, p)
		if rerr != nil {
			return fmt.Errorf("backup: blob path: %w", rerr)
		}
		return appendEntry(m, filepath.ToSlash(rel), p, 0o600)
	})
	if err != nil {
		return fmt.Errorf("backup: blobs: %w", err)
	}
	return nil
}

func appendOptional(m *Manifest, rel, abs string) error {
	if _, err := os.Stat(abs); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("backup: stat %s: %w", abs, err)
	}
	return appendEntry(m, rel, abs, 0o600)
}

func appendEntry(m *Manifest, rel, abs string, mode uint32) error {
	fi, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("backup: stat %s: %w", abs, err)
	}
	h, err := hashFile(abs)
	if err != nil {
		return err
	}
	m.Entries = append(m.Entries, Entry{Path: rel, Size: fi.Size(), Mode: mode, SHA256: h})
	return nil
}

func hashFile(p string) (sum string, err error) {
	f, err := os.Open(filepath.Clean(p))
	if err != nil {
		return "", fmt.Errorf("backup: open %s: %w", p, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", fmt.Errorf("backup: hash %s: %w", p, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func publish(ctx context.Context, o *CreateOptions, manifest []byte, snap string) (res Result, err error) {
	out := o.Out
	if out == "" {
		var r [4]byte
		if _, rerr := rand.Read(r[:]); rerr != nil {
			return res, fmt.Errorf("backup: random name: %w", rerr)
		}
		name := fmt.Sprintf("backup-%s-%s.dmbk", o.Now.UTC().Format("20060102T150405Z"), hex.EncodeToString(r[:]))
		out = filepath.Join(o.DataDir, "backups", name)
	}
	if _, lerr := os.Lstat(out); lerr == nil {
		return res, fmt.Errorf("backup: %s exists: %w", out, ErrCorrupt)
	}
	partial := out + ".partial"
	f, err := os.OpenFile(filepath.Clean(partial), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return res, fmt.Errorf("backup: create %s: %w", partial, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeFile(partial))
		}
	}()
	sum := sha256.Sum256(manifest)
	w, err := NewWriter(f, o.Key, &WriterOptions{
		CreatedAt: o.Now, App: o.App, IncludeSecrets: o.IncludeSecrets, ManifestSHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return res, errors.Join(err, f.Close())
	}
	if werr := writePayload(ctx, w, manifest, o, snap); werr != nil {
		return res, errors.Join(werr, f.Close())
	}
	if cerr := w.Close(); cerr != nil {
		return res, errors.Join(cerr, f.Close())
	}
	if ferr := errors.Join(f.Sync(), f.Close()); ferr != nil {
		return res, fmt.Errorf("backup: finish %s: %w", partial, ferr)
	}
	if rerr := os.Rename(partial, out); rerr != nil {
		return res, fmt.Errorf("backup: publish %s: %w", out, rerr)
	}
	fi, err := os.Stat(out)
	if err != nil {
		return res, fmt.Errorf("backup: stat %s: %w", out, err)
	}
	return Result{Path: out, Header: Header{
		Version: formatVersion, CreatedAt: o.Now.UTC().Unix(), App: o.App,
		IncludeSecrets: o.IncludeSecrets, Chunk: chunkSize, RecipientID: o.Key.ID,
		ManifestSHA256: hex.EncodeToString(sum[:]),
	}, Size: fi.Size()}, nil
}

func writePayload(ctx context.Context, w *Writer, manifest []byte, o *CreateOptions, snap string) error {
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(manifest))) //nolint:gosec // manifest <= 16 MiB
	if _, err := w.Write(lb[:]); err != nil {
		return err
	}
	if _, err := w.Write(manifest); err != nil {
		return err
	}
	m, err := decodeManifest(manifest)
	if err != nil {
		return err
	}
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		abs, aerr := entryPath(o, e.Path, snap)
		if aerr != nil {
			return aerr
		}
		if err := copyEntry(w, abs, e.Size); err != nil {
			return err
		}
	}
	return nil
}

func entryPath(o *CreateOptions, rel, snap string) (string, error) {
	switch rel {
	case "damnbot.db":
		return snap, nil
	case "config.toml", "secrets.enc":
		return filepath.Join(o.ConfigDir, rel), nil
	case "storage.key":
		return filepath.Join(o.DataDir, rel), nil
	}
	p := filepath.Join(o.DataDir, filepath.FromSlash(rel))
	root := filepath.Clean(o.DataDir)
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("backup: path escapes data dir: %w", ErrCorrupt)
	}
	return p, nil
}

func copyEntry(w *Writer, p string, size int64) (err error) {
	f, err := os.Open(filepath.Clean(p))
	if err != nil {
		return fmt.Errorf("backup: open %s: %w", p, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	n, err := io.CopyN(w, f, size)
	if err != nil || n != size {
		return fmt.Errorf("backup: copy %s: %w", p, errors.Join(err, io.ErrUnexpectedEOF))
	}
	return nil
}

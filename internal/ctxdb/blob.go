package ctxdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// PendingBlob is a temp file written by PutBlob, published later inside the
// owner's transaction (§11.6 Blob).
type PendingBlob struct {
	Sha, Mime string
	Size      int64
	tmp       string
}

func (s *Store) blobPath(sha string) string { return filepath.Join(s.dir, "blobs", sha[:2], sha) }
func (s *Store) trashDir(txid string) string { return filepath.Join(s.dir, "blobs", ".trash", txid) }

// PutBlob writes blobs/tmp/<rand> and fsyncs it outside any lock.
func (s *Store) PutBlob(data []byte, mime string) (*PendingBlob, error) {
	h := sha256.Sum256(data)
	p := &PendingBlob{Sha: hex.EncodeToString(h[:]), Mime: mime, Size: int64(len(data)), tmp: filepath.Join(s.dir, "blobs", "tmp", newID())}
	if err := writeAtomic(p.tmp, data); err != nil { // temp + fsync + rename into tmp/
		return nil, err
	}
	return p, nil
}

// Publish renames the temp file to its final path under the write lock,
// fsyncs the parent, inserts the blobs row and, when the sha's latest status
// is expired, a `restored{rewrite}` row (TestBlobRewriteAfterGC).
func (t *Tx) Publish(p *PendingBlob) error {
	final := t.s.blobPath(p.Sha)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return fmt.Errorf("ctxdb: publish: %w", err)
	}
	if _, err := os.Stat(final); err == nil {
		_ = os.Remove(p.tmp)
	} else if err := os.Rename(p.tmp, final); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ctxdb: publish: %w", err)
	}
	if err := syncDir(filepath.Dir(final)); err != nil {
		return err
	}
	now := t.s.clk.WallMs()
	if _, err := t.Exec("INSERT INTO blobs(sha256, size, mime, created_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING",
		p.Sha, p.Size, p.Mime, now); err != nil {
		return err
	}
	_, err := t.Exec(`INSERT INTO blob_expirations(sha256, kind, reason, at)
		SELECT ?1, 'restored', 'rewrite', ?2 WHERE (SELECT kind FROM blob_expirations WHERE sha256 = ?1 ORDER BY id DESC LIMIT 1) = 'expired'`,
		p.Sha, now)
	return err
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("ctxdb: fsync dir: %w", err)
	}
	err = f.Sync()
	return errors.Join(err, f.Close())
}

// ReadBlob reads a blob. On ENOENT it re-reads the status: expired →
// blob_expired; live → recover this sha from .trash (gc step 1 for one sha)
// under the write lock and read once more; then blob_missing.
func (s *Store) ReadBlob(ctx context.Context, sha string) ([]byte, error) {
	b, err := os.ReadFile(s.blobPath(sha))
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return b, err
	}
	var last string
	if err := s.get(ctx, "SELECT coalesce((SELECT kind FROM blob_expirations WHERE sha256 = ? ORDER BY id DESC LIMIT 1), '')", []any{sha}, &last); err != nil {
		return nil, err
	}
	if last == "expired" {
		return nil, fmt.Errorf("ctxdb: blob %s: %w", sha, ErrBlobExpired)
	}
	if err := s.Write(ctx, func(t *Tx) error { return t.recoverTrash("", sha) }); err != nil {
		return nil, err
	}
	if b, err = os.ReadFile(s.blobPath(sha)); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("ctxdb: blob %s: %w", sha, ErrBlobMissing)
	}
	return b, err
}

// recoverTrash is gc step 1, always under the write lock: every owner of a
// .trash/<txid>/ visible now has committed or aborted. A file goes back only
// if its blobs row exists, its latest status is not expired and the final
// path is free; otherwise it is deleted. txid/sha "" = all.
func (t *Tx) recoverTrash(txid, sha string) error {
	root := filepath.Join(t.s.dir, "blobs", ".trash")
	dirs, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("ctxdb: trash: %w", err)
	}
	for _, d := range dirs {
		if txid != "" && d.Name() != txid {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(root, d.Name()))
		for _, f := range files {
			if sha != "" && f.Name() != sha {
				continue
			}
			if err := t.restoreOrDrop(filepath.Join(root, d.Name(), f.Name()), f.Name()); err != nil {
				return err
			}
		}
		_ = os.Remove(filepath.Join(root, d.Name())) // only succeeds when empty
	}
	return nil
}

func (t *Tx) restoreOrDrop(src, sha string) error {
	final := t.s.blobPath(sha)
	_, statErr := os.Stat(final)
	if t.checkLiveBlob(sha) == nil && errors.Is(statErr, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
			return fmt.Errorf("ctxdb: trash restore: %w", err)
		}
		if err := os.Rename(src, final); err != nil {
			return fmt.Errorf("ctxdb: trash restore: %w", err)
		}
		return syncDir(filepath.Dir(final))
	}
	return os.Remove(src)
}

// trashFile moves a final blob file into this transaction's trash dir.
func (t *Tx) trashFile(sha string) error {
	dir := t.s.trashDir(t.txid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ctxdb: trash: %w", err)
	}
	if err := os.Rename(t.s.blobPath(sha), filepath.Join(dir, sha)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ctxdb: trash %s: %w", sha, err)
	}
	t.OnCommit(func() { _ = os.RemoveAll(dir) })
	return nil
}

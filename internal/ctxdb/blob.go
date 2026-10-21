package ctxdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"syscall"
)

// PendingBlob is a temp file written by PutBlob, published later inside the
// owner's transaction (§11.6 Blob).
type PendingBlob struct {
	Sha, Mime string
	Size      int64
	tmp       string
}

func blobName(sha string) string   { return path.Join("blobs", sha[:2], sha) }
func trashName(txid string) string { return path.Join("blobs/.trash", txid) }

// PutBlob writes blobs/tmp/<rand> and fsyncs it outside any lock.
func (s *Store) PutBlob(data []byte, mime string) (*PendingBlob, error) {
	h := sha256.Sum256(data)
	p := &PendingBlob{Sha: hex.EncodeToString(h[:]), Mime: mime, Size: int64(len(data)), tmp: path.Join("blobs/tmp", newID())}
	stored := data
	if s.codec != nil {
		sealed, err := s.codec.Seal("blob:"+p.Sha, data)
		if err != nil {
			return nil, fmt.Errorf("ctxdb: seal blob: %w", err)
		}
		stored = sealed
	}
	if err := writeAtomic(s.root, p.tmp, stored); err != nil {
		return nil, err
	}
	return p, nil
}

// Publish renames the temp file to its final path under the write lock,
// fsyncs the parent, inserts the blobs row and, when the sha's latest status
// is expired, a `restored{rewrite}` row (TestBlobRewriteAfterGC).
func (t *Tx) Publish(p *PendingBlob) error {
	r, final := t.s.root, blobName(p.Sha)
	err := r.MkdirAll(path.Dir(final), 0o700)
	if _, serr := r.Stat(final); err == nil && serr == nil {
		err = removeIfExists(r, p.tmp) // content-addressed: same bytes already published
	} else if err == nil {
		err = r.Rename(p.tmp, final)
	}
	if err == nil {
		err = t.syncDir(path.Dir(final))
	}
	if err != nil {
		return wrap(err)
	}
	_, err = t.Exec(`INSERT INTO blobs(sha256, size, mime, created_at) VALUES (?1,?2,?3,?4) ON CONFLICT DO NOTHING;
		INSERT INTO blob_expirations(sha256, kind, reason, at) SELECT ?1, 'restored', 'rewrite', ?4
		 WHERE (SELECT kind FROM blob_expirations WHERE sha256 = ?1 ORDER BY id DESC LIMIT 1) = 'expired'`,
		p.Sha, p.Size, p.Mime, t.s.clk.WallMs())
	return err
}

func (t *Tx) syncDir(dir string) error { return syncRootDir(t.s.root, dir) }

// syncRootDir fsyncs a directory of the data dir (blob publication, spool);
// the directory must exist on every OS (see syncDirFile for Windows).
func syncRootDir(root *os.Root, dir string) error {
	f, err := root.Open(dir)
	if err != nil {
		return wrap(err)
	}
	return wrap(errors.Join(syncDirFile(f), f.Close()))
}

// ReadBlob reads a blob. On ENOENT it re-reads the status: expired →
// blob_expired; live → recover this sha from .trash (gc step 1 for one sha)
// under the write lock and read once more; then blob_missing.
func (s *Store) ReadBlob(ctx context.Context, sha string) ([]byte, error) {
	b, err := s.readBlob(ctx, sha)
	if err != nil {
		return nil, err
	}
	if s.codec == nil {
		return b, nil
	}
	pt, err := s.codec.Open("blob:"+sha, b)
	if err != nil {
		return nil, fmt.Errorf("ctxdb: open blob: %w", err)
	}
	sum := sha256.Sum256(pt)
	if hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("ctxdb: blob %s hash: %w", sha, ErrInternal)
	}
	return pt, nil
}

func (s *Store) readBlob(ctx context.Context, sha string) ([]byte, error) {
	b, err := s.root.ReadFile(blobName(sha))
	if !errors.Is(err, fs.ErrNotExist) {
		return b, wrap(err)
	}
	var last string
	if gerr := s.get(ctx, "SELECT coalesce((SELECT kind FROM blob_expirations WHERE sha256 = ? ORDER BY id DESC LIMIT 1), '')", []any{sha}, &last); gerr != nil {
		return nil, gerr
	}
	if last == "expired" {
		return nil, fmt.Errorf("ctxdb: blob %s: %w", sha, ErrBlobExpired)
	}
	if werr := s.Write(ctx, func(t *Tx) error { return t.recoverTrash("", sha) }); werr != nil {
		return nil, werr
	}
	if b, err = s.root.ReadFile(blobName(sha)); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("ctxdb: blob %s: %w", sha, ErrBlobMissing)
	}
	return b, wrap(err)
}

// recoverTrash is gc step 1, always under the write lock: every owner of a
// .trash/<txid>/ visible now has committed or aborted. A file goes back only
// if its blobs row exists, its latest status is not expired and the final
// path is free; otherwise it is deleted. txid/sha "" = all.
func (t *Tx) recoverTrash(txid, sha string) error {
	fsys := t.s.root.FS()
	dirs, err := fs.ReadDir(fsys, "blobs/.trash")
	for i := 0; err == nil && i < len(dirs); i++ {
		if txid != "" && dirs[i].Name() != txid {
			continue
		}
		dir := trashName(dirs[i].Name())
		files, rerr := fs.ReadDir(fsys, dir)
		err = rerr
		for j := 0; err == nil && j < len(files); j++ {
			if sha == "" || files[j].Name() == sha {
				err = t.restoreOrDrop(path.Join(dir, files[j].Name()), files[j].Name())
			}
		}
		if rerr := t.s.root.Remove(dir); err == nil && rerr != nil && !errors.Is(rerr, syscall.ENOTEMPTY) {
			err = rerr // a dir still holding other shas stays
		}
	}
	return wrap(err)
}

func (t *Tx) restoreOrDrop(src, sha string) error {
	r, final := t.s.root, blobName(sha)
	if _, serr := r.Stat(final); t.checkLiveBlob(sha) != nil || !errors.Is(serr, fs.ErrNotExist) {
		return wrap(r.Remove(src))
	}
	if err := r.MkdirAll(path.Dir(final), 0o700); err != nil {
		return wrap(err)
	}
	if err := r.Rename(src, final); err != nil {
		return wrap(err)
	}
	return t.syncDir(path.Dir(final))
}

// trashFile moves a final blob file into this transaction's trash dir; the
// dir is unlinked after COMMIT and recovered after a rollback.
func (t *Tx) trashFile(sha string) error {
	r, dir := t.s.root, trashName(t.txid)
	if err := r.MkdirAll(dir, 0o700); err != nil {
		return wrap(err)
	}
	if err := r.Rename(blobName(sha), path.Join(dir, sha)); err != nil {
		return wrap(err)
	}
	if !t.trashed {
		t.OnCommit(func() error { return r.RemoveAll(dir) })
	}
	t.trashed = true
	return nil
}

package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// FileSpill is the F1 file-backed spill store: <dir>/blobs/<sha[0:2]>/<sha>
// written through blobs/tmp + fsync + rename (SPEC §11.6). It never touches
// workspace paths, so it is outside the sandbox boundary by design.
type FileSpill struct{ dir string }

// NewFileSpill returns a store rooted at dataDir.
func NewFileSpill(dataDir string) *FileSpill { return &FileSpill{dir: dataDir} }

// Put writes data atomically and idempotently.
func (s *FileSpill) Put(ctx context.Context, data []byte) (BlobRef, error) {
	if s == nil || s.dir == "" {
		return BlobRef{}, errors.New("tools: spill directory is empty")
	}
	if err := ctx.Err(); err != nil {
		return BlobRef{}, err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	final := filepath.Join(s.dir, "blobs", sha[:2], sha)
	ref := BlobRef{SHA: sha, Path: final, Size: int64(len(data))}
	if fi, err := os.Stat(final); err == nil && fi.Mode().IsRegular() {
		return ref, nil
	}
	tmpDir := filepath.Join(s.dir, "blobs", "tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return BlobRef{}, spillErr("mkdir", err)
	}
	name, err := tempName()
	if err != nil {
		return BlobRef{}, spillErr("temp name", err)
	}
	tmp := filepath.Join(tmpDir, name)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return BlobRef{}, spillErr("create", err)
	}
	if _, err = f.Write(data); err != nil {
		return BlobRef{}, spillErr("write", errors.Join(err, discard(f, tmp)))
	}
	if err = f.Sync(); err != nil {
		return BlobRef{}, spillErr("sync", errors.Join(err, discard(f, tmp)))
	}
	if err = f.Close(); err != nil {
		return BlobRef{}, spillErr("close", errors.Join(err, remove(tmp)))
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return BlobRef{}, spillErr("mkdir", errors.Join(err, remove(tmp)))
	}
	if err := os.Rename(tmp, final); err != nil {
		return BlobRef{}, spillErr("rename", errors.Join(err, remove(tmp)))
	}
	if err := syncDir(filepath.Dir(final)); err != nil {
		return BlobRef{}, spillErr("fsync", err)
	}
	return ref, nil
}

// Get reads one blob by hex sha256.
func (s *FileSpill) Get(ctx context.Context, sha string) ([]byte, error) {
	if len(sha) != 64 {
		return nil, fmt.Errorf("tools: invalid blob sha %q", sha)
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return nil, fmt.Errorf("tools: invalid blob sha %q", sha)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "blobs", sha[:2], sha))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &ToolError{Code: CodeBlobMissing, Message: "blob " + sha + " is missing", Err: err}
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != sha {
		return nil, &ToolError{Code: CodeInternal, Message: "blob " + sha + " failed its content hash", Err: err}
	}
	return b, nil
}

// discard closes f and removes its temp file, joining both failures.
func discard(f *os.File, name string) error {
	return errors.Join(f.Close(), remove(name))
}

// remove deletes a temp file, tolerating an already missing name.
func remove(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func tempName() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// spillErr maps storage failures onto the closed tool-code set.
func spillErr(op string, err error) error {
	code := CodeInternal
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		code = CodeDiskFull
	}
	return &ToolError{Code: code, Message: "spill " + op + ": " + err.Error(), Err: err}
}

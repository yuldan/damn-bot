package testutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteExecutable writes data to path through a sibling temp file and an
// install step that never opens the destination for writing (Unix rename; on
// Windows remove-then-rename, since MoveFileEx cannot replace an open name).
// A process forked while the bytes land therefore cannot inherit a writer for
// the file the test is about to execute.
func WriteExecutable(path string, data []byte, perm fs.FileMode) (err error) {
	f, cerr := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if cerr != nil {
		return fmt.Errorf("create temp for %s: %w", path, cerr)
	}
	tmp := f.Name()
	defer func() {
		if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove %s: %w", tmp, rerr))
		}
	}()
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("close %s: %w", tmp, cerr)
	}
	if werr := os.WriteFile(tmp, data, perm); werr != nil {
		return fmt.Errorf("write %s: %w", tmp, werr)
	}
	if herr := os.Chmod(tmp, perm); herr != nil {
		return fmt.Errorf("chmod %s: %w", tmp, herr)
	}
	return replaceFile(tmp, path)
}

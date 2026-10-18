package sandbox

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// openBeneath on Windows: os.Root resolves rel handle-relative and never
// leaves dir (symlinks and junctions included); then the handle's final
// path (GetFinalPathNameByHandle, junctions resolved, `\\?\` normalized,
// case-insensitive) must be exactly want, so a link swapped in after Check
// — even one pointing inside the root — fails with ErrOutsideWorkspace.
// Known false positive (fail-closed): mapped/subst drives, whose final path
// is the UNC/target volume.
func (s *Sandbox) openBeneath(dir, rel, want string, flag int, perm fs.FileMode) (*os.File, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	defer closeDir(r)
	f, err := r.OpenFile(rel, flag, perm)
	if err != nil {
		// rel is a resolved path without links (Check). os.Root refuses a
		// link escaping dir with an unexported error, so classify by state,
		// not text (E-21): a link now on the path = lost race.
		if linkOnPath(r, rel) {
			return nil, escaped(want, err)
		}
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	got, err := finalPath(f)
	switch {
	case err == nil && sameWindowsPath(got, want):
		return f, nil
	case err == nil && s.denied(normalizeWindows(got)):
		return nil, closeFail(f, ErrDeniedPath) // alias of a deny path (S16-2)
	case err == nil:
		err = fs.ErrInvalid
	}
	return nil, closeFail(f, escaped(want, err))
}

// handlePath is the normalized final path of f (GetFinalPathNameByHandle).
func handlePath(f *os.File) (string, bool) {
	p, err := finalPath(f)
	return normalizeWindows(p), err == nil
}

// finalPathFlags = FILE_NAME_NORMALIZED|VOLUME_NAME_DOS (both 0 in the
// Win32 API; x/sys/windows does not export them).
const finalPathFlags = 0

// finalPath returns the DOS final path of f (`\\?\` prefix included).
func finalPath(f *os.File) (string, error) {
	h := windows.Handle(f.Fd())
	size := uint32(windows.MAX_PATH)
	for {
		buf := make([]uint16, size)
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], size, finalPathFlags)
		if err != nil {
			return "", fmt.Errorf("sandbox: GetFinalPathNameByHandle: %w", err)
		}
		if n < size {
			return windows.UTF16ToString(buf[:n]), nil
		}
		size = n + 1 // too small: n is the required size incl. NUL
	}
}

// linkOnPath reports whether a component of rel beneath r is now a symlink
// or junction (any non-regular, non-directory entry is treated as a link).
func linkOnPath(r *os.Root, rel string) bool {
	p := ""
	for c := range strings.SplitSeq(filepath.Clean(rel), string(filepath.Separator)) {
		p = filepath.Join(p, c)
		fi, err := r.Lstat(p)
		if err != nil {
			return false
		}
		if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return true
		}
	}
	return false
}

package codetools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// linkCount is the hard-link count of f (1 when unknown).
func linkCount(f *os.File) uint64 {
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info) != nil {
		return 1
	}
	return uint64(info.NumberOfLinks)
}

// copyOwner: the replacing file gets the creator as owner and the
// directory's inherited ACL, so an explicit owner or ACL on the old file
// is not kept; reported as owner_preserved=false (CT27-12).
func copyOwner(_, _ *os.File) bool { return false }

// copyXattrs: no extended attributes on Windows.
func copyXattrs(_, _ *os.File) bool { return true }

// removeAt removes name in the directory d (by path: Windows has no
// unlinkat; d was opened and verified through the sandbox).
func removeAt(d *os.File, name string) error {
	if err := os.Remove(filepath.Join(d.Name(), name)); err != nil {
		return fmt.Errorf("codetools: remove: %w", err)
	}
	return nil
}

// renameReplace replaces dst with MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)
// (CODE-TOOLS §5.4).
func renameReplace(src, dst string) error {
	s, err := windows.UTF16PtrFromString(src)
	if err == nil {
		var d *uint16
		if d, err = windows.UTF16PtrFromString(dst); err == nil {
			err = windows.MoveFileEx(s, d, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		}
	}
	if err != nil {
		return fmt.Errorf("codetools: MoveFileEx: %w", err)
	}
	return nil
}

// dirSyncable: directory handles cannot be flushed on Windows and NTFS
// journals directory metadata, so fsync(dir) is a no-op (ADR-094 butir 15).
const dirSyncable = false

func isDiskFull(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}

package sandbox

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// linkCountPath is the link count of the regular file at p, read through a
// handle opened for attributes only, without following a reparse point;
// 1 for directories (S16-7).
func linkCountPath(p string) (uint64, error) {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0, fmt.Errorf("sandbox: path: %w", err)
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, fmt.Errorf("sandbox: CreateFile: %w", err)
	}
	defer closeDir(handleCloser(h))
	return handleLinks(h)
}

// handleCloser adapts an attribute-only handle to io.Closer for closeDir:
// nothing was written through it, so a close error cannot lose data.
type handleCloser windows.Handle

func (h handleCloser) Close() error {
	if err := windows.CloseHandle(windows.Handle(h)); err != nil {
		return fmt.Errorf("sandbox: close: %w", err)
	}
	return nil
}

// linkCountFile is linkCountPath for an open handle.
func linkCountFile(f *os.File) (uint64, error) {
	return handleLinks(windows.Handle(f.Fd()))
}

// handleLinks reads NumberOfLinks (GetFileInformationByHandle).
func handleLinks(h windows.Handle) (uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return 0, fmt.Errorf("sandbox: GetFileInformationByHandle: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return 1, nil
	}
	return uint64(info.NumberOfLinks), nil
}

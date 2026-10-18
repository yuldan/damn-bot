package sandbox

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// linkCountPath is the link count of the regular file at p (resolved, so
// free of reparse points); 1 for directories (S16-7). A file that cannot
// be opened for reading reports an error and is judged after open, by
// linkCountFile on the handle OpenFile obtains.
func linkCountPath(p string) (uint64, error) {
	f, err := os.Open(p) //nolint:gosec // G304: p is the sandbox-resolved path being classified.
	if err != nil {
		return 0, fmt.Errorf("sandbox: open: %w", err)
	}
	defer closeDir(f)
	return linkCountFile(f, nil)
}

// linkCountFile reads NumberOfLinks (GetFileInformationByHandle).
func linkCountFile(f *os.File, _ os.FileInfo) (uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return 0, fmt.Errorf("sandbox: GetFileInformationByHandle: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return 1, nil
	}
	return uint64(info.NumberOfLinks), nil
}

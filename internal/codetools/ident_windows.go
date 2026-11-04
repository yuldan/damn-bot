//go:build windows

package codetools

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// identify returns the volume serial number and 64-bit file index of path
// from a handle (CT27-5). os.FileInfo carries no file index on Windows,
// and creation time — the old stand-in — is not unique: unrelated files
// written in the same clock tick share it, so two distinct files could end
// up under one edit lock. A handle that cannot be opened or queried fails
// closed with IdentityError; size and mtime are never substituted.
func identify(path string) (fileID, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileID{}, &IdentityError{Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	ierr := errors.Join(windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info), f.Close())
	if ierr != nil {
		return fileID{}, &IdentityError{Path: path, Err: ierr}
	}
	return fileID{Vol: uint64(info.VolumeSerialNumber), Hi: uint64(info.FileIndexHigh), Lo: uint64(info.FileIndexLow)}, nil
}

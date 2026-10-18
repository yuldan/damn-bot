//go:build linux || darwin

package codetools

import (
	"bytes"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// copyXattrs copies every extended attribute (SELinux label included) from
// src to dst; false when one could not be copied (P14).
func copyXattrs(src, dst *os.File) bool {
	sfd, dfd := int(src.Fd()), int(dst.Fd())
	sz, err := unix.Flistxattr(sfd, nil)
	if err != nil || sz == 0 {
		return err == nil || errors.Is(err, unix.ENOTSUP)
	}
	names := make([]byte, sz)
	if sz, err = unix.Flistxattr(sfd, names); err != nil {
		return false
	}
	ok := true
	for n := range bytes.SplitSeq(names[:sz], []byte{0}) {
		if len(n) == 0 {
			continue
		}
		vs, err := unix.Fgetxattr(sfd, string(n), nil)
		if err != nil {
			ok = false
			continue
		}
		v := make([]byte, vs)
		if vs, err = unix.Fgetxattr(sfd, string(n), v); err != nil || unix.Fsetxattr(dfd, string(n), v[:vs], 0) != nil {
			ok = false
		}
	}
	return ok
}

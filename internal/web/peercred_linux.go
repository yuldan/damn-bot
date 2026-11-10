//go:build linux

package web

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// sameUID reports whether the unix peer belongs to this process's UID
// (GAP-SEC-07: SO_PEERCRED). A peer that cannot be inspected is refused.
func sameUID(c net.Conn) bool {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || cerr != nil || cred == nil {
		return false
	}
	return int(cred.Uid) == syscall.Getuid()
}

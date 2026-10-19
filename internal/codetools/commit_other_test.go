//go:build !windows

package codetools

import "syscall"

// diskFullErr is the error the OS reports when a write fills the volume.
func diskFullErr() error { return syscall.ENOSPC }

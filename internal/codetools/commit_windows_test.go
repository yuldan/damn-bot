//go:build windows

package codetools

import "golang.org/x/sys/windows"

// diskFullErr is the error the OS reports when a write fills the volume.
func diskFullErr() error { return windows.ERROR_DISK_FULL }

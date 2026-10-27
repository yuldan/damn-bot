//go:build darwin

package update

import (
	"runtime"

	"golang.org/x/sys/unix"
)

func nativeArch() (string, error) {
	if translated, err := unix.SysctlUint32("sysctl.proc_translated"); err == nil && translated == 1 {
		return "arm64", nil
	}
	return runtime.GOARCH, nil
}

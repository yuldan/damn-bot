//go:build linux

package update

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func nativeArch() (string, error) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", fmt.Errorf("uname: %w", err)
	}
	switch m := unix.ByteSliceToString(u.Machine[:]); m {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	case "armv7l", "armv8l":
		return "arm", nil
	default:
		return "", fmt.Errorf("unsupported native machine %q", m)
	}
}

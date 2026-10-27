//go:build windows

package update

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

func nativeArch() (string, error) {
	handle, err := windows.GetCurrentProcess()
	if err != nil {
		return runtime.GOARCH, nil
	}
	var process, native uint16
	if err := windows.IsWow64Process2(handle, &process, &native); err != nil {
		return runtime.GOARCH, nil
	}
	switch native {
	case 0x8664:
		return "amd64", nil
	case 0xaa64:
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported native machine 0x%04x", native)
	}
}

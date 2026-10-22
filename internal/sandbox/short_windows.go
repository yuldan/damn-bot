//go:build windows

package sandbox

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// shortFinal returns the 8.3 spelling of an existing path ("" when the volume
// disabled 8.3 or the call fails). Roots keep it so a path recorded in short
// form maps back to the canonical root (SEC14).
func shortFinal(p string) (string, error) {
	longp, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, serr := windows.GetShortPathName(longp, &buf[0], uint32(len(buf)))
		if serr != nil {
			return "", fmt.Errorf("sandbox: GetShortPathName: %w", serr)
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		buf = make([]uint16, n+1)
	}
}

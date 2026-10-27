//go:build !linux && !darwin && !windows

package update

import "runtime"

func nativeArch() (string, error) { return runtime.GOARCH, nil }

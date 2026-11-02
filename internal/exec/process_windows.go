//go:build windows && !minimal

package exec

import (
	"context"
	"errors"
	"fmt"
)

// BackgroundSupported declares whether the background registry can start a
// process on this platform. Windows needs a Job Object to own the tree; this
// slice does not wire one, so Start refuses with ErrCapability instead of
// detaching a process that Close cannot kill.
const BackgroundSupported = false

var errBackgroundWindows = errors.New("exec: background processes need a Job Object (unsupported on Windows in this slice)")

func startBackground(context.Context, spawnSpec) (*cmdHandle, error) {
	return nil, fmt.Errorf("%w: %w", ErrCapability, errBackgroundWindows)
}

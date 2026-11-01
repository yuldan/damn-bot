//go:build windows

package exec

import (
	"context"
	"errors"
	"fmt"
)

// Capability declaration: the background registry is not available on
// Windows in this slice because it needs a Job Object; Start fails closed
// instead of detaching a process that Close cannot kill.
var errBackgroundWindows = errors.New("exec: background processes need a Job Object (unsupported on Windows in this slice)")

func startBackground(context.Context, spawnSpec) (*cmdHandle, error) {
	return nil, fmt.Errorf("%w: %w", ErrProcessLimit, errBackgroundWindows)
}

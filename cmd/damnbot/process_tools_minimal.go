//go:build minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/tools"
)

// withProcessTools is absent in the minimal build.
func withProcessTools() ([]tools.Tool, func(context.Context) error, error) {
	return nil, nil, nil
}

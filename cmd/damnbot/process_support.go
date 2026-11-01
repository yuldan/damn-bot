package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/tools"
)

// processSupport is the optional background-process wiring of one run; the
// minimal build returns the zero value so neither the registry nor the
// adapters are linked.
type processSupport struct {
	tools  []tools.Tool
	close  func(context.Context) error
	attach func(*tools.ShellConfig)
}

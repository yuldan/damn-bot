//go:build !minimal

package main

import (
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/tools"
)

// processToolsLinked reports whether the process_* adapters are wired.
const processToolsLinked = true

// withProcessTools creates the background registry and its adapters (F5).
// The registry is !minimal so the minimal binary does not link it.
func withProcessTools() []tools.Tool {
	return tools.NewProcessTools(&tools.ProcessConfig{Registry: exec.NewBackground()})
}

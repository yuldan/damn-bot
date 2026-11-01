//go:build !minimal

package main

import (
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/tools"
)

// withProcessTools appends the process_* adapters (F5). They are !minimal so
// the minimal binary does not link the registry tool layer.
func withProcessTools(bg *exec.Background) []tools.Tool {
	return tools.NewProcessTools(&tools.ProcessConfig{Registry: bg})
}

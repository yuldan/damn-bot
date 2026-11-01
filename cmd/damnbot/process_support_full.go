//go:build !minimal

package main

import (
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/tools"
)

// newProcessSupport creates the background registry, its process_* adapters
// and the shell background attachment (F5, ADR-072/278/292).
func newProcessSupport() (processSupport, error) {
	bg := exec.NewBackground()
	return processSupport{
		tools: tools.NewProcessTools(&tools.ProcessConfig{Registry: bg}),
		close: bg.Close,
		attach: func(c *tools.ShellConfig) {
			c.Background = bg
		},
	}, nil
}

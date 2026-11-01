//go:build !minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/tools"
)

// processToolsLinked reports whether the background registry is compiled in.
const processToolsLinked = true

// processSupport is the background-process wiring of one run.
type processSupport struct {
	tools  []tools.Tool
	close  func(context.Context) error
	attach func(*tools.ShellConfig)
}

func (p processSupport) toolList() []tools.Tool               { return p.tools }
func (p processSupport) closeFn() func(context.Context) error { return p.close }
func (p processSupport) attachFn() func(*tools.ShellConfig)   { return p.attach }

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

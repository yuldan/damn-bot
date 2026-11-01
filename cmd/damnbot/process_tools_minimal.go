//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/tools"
)

// withProcessTools is empty in the minimal build.
func withProcessTools(*exec.Background) []tools.Tool { return nil }

//go:build minimal

package main

import "github.com/damnbot/damnbot/internal/tools"

// withCodeIntelTools is absent in the minimal build.
func withCodeIntelTools(_ []string, _ string, _ bool, list []tools.Tool) []tools.Tool {
	return list
}

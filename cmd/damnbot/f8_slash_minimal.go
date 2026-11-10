//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/tui"
)

// The minimal build links neither the F8 adapter nor its modules; both seams
// stay nil so every F8 slash answers module_unavailable.
func newGatewayF8(string) gateway.F8Commands { return nil }
func newTUIF8(string) tui.SlashF8            { return nil }

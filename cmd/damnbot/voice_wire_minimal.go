//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/tui"
)

// The minimal build has no [modules.voice] section; both commands answer
// module_unavailable instead of pretending to have a backend.
func newTUIVoice(*config.Config) tui.Voice         { return nil }
func newGatewayVoice(*config.Config) gateway.Voice { return nil }

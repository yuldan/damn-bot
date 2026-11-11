//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/config"
)

// stableReply is empty in the minimal build: reply_language is a full-build
// surface, so no language strings or resolution code are linked (GAP-INF-09).
func stableReply(*config.Config) assemble.StableOptions { return assemble.StableOptions{} }

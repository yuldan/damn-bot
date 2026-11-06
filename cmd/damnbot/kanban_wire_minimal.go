//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/tools"
)

func withKanbanTools(list []tools.Tool, _ *config.Config, _ *ctxdb.Store, _ string) []tools.Tool {
	return list
}

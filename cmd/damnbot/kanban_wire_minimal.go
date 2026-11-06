//go:build minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/tools"
)

// The minimal build has no kanban surface or [kanban] automation; serve does
// no board work and never links internal/kanban.
func withKanbanTools(list []tools.Tool, _ *config.Config, _ *ctxdb.Store, _ string) []tools.Tool {
	return list
}

func startKanbanAutomation(_ context.Context, _ *serveGateway, _ *ctxdb.Store) {}

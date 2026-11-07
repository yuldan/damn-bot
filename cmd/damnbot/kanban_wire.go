//go:build !minimal

package main

import (
	"cmp"
	"runtime"
	"slices"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/kanban"
	"github.com/damnbot/damnbot/internal/tools"
	"github.com/damnbot/damnbot/internal/toolset"
)

// withKanbanTools adds the 15 kanban tools only when the config selects the
// kanban toolset. The dispatcher environment supplies the board/task ids;
// outside that mode every task-scoped tool fails closed with
// module_unavailable and no board is read.
func withKanbanTools(list []tools.Tool, cfg *config.Config, db *ctxdb.Store, cwd string) []tools.Tool {
	// An empty [toolsets] keeps the F1 eager payload: "every tool" there means
	// every tool that was exposed before toolsets existed, so the default-off
	// kanban bundle must not leak in the way Resolve would return it.
	if len(cfg.Toolsets.Active) == 0 && len(cfg.Toolsets.Disabled) == 0 {
		return list
	}
	names := make(map[string]bool, 15)
	for _, t := range kanban.NewTools(nil, "", "", "", false) {
		names[t.Name()] = true
	}
	sel, err := toolset.Resolve(cfg.Toolsets, runtime.GOOS, names)
	if err != nil || !slices.Contains(sel.Tools, "kanban_show") {
		return list
	}
	board := cmp.Or(config.OS().Getenv("HERMES_KANBAN_BOARD"), "default")
	return append(list, kanban.NewTools(kanban.New(db, cwd), board, config.OS().Getenv("HERMES_KANBAN_TASK"),
		"agent", config.OS().Getenv("HERMES_KANBAN_ORCHESTRATOR") == "1")...)
}

//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/kanban"
)

// kanbanGlobal holds the flags shared by all 45 CLI-029 subcommands. They may
// appear before or after the subcommand: the small scanner below removes them
// and leaves the subcommand's own flags untouched.
type kanbanGlobal struct {
	configPath, dataDir, workspace, board, actor string
	json                                         bool
}

func runKanban(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		kanbanUsage(stdout)
		return 0
	}
	var g kanbanGlobal
	g.board, g.actor = "default", "cli"
	rest, err := scanKanbanGlobals(args, &g)
	if err != nil {
		sessionsPrintf(stderr, "kanban: %s\n", cleanLine(err.Error()))
		return 2
	}
	if len(rest) == 0 {
		kanbanUsage(stderr)
		return 2
	}
	sub, subArgs := rest[0], rest[1:]
	if sub == "help" {
		kanbanUsage(stdout)
		return 0
	}
	if !slices.Contains(kanban.Subcommands(), sub) {
		sessionsPrintf(stderr, "kanban: unknown subcommand %q\n", cleanArg(sub))
		kanbanUsage(stderr)
		return 2
	}
	if g.workspace == "" {
		if wd, werr := os.Getwd(); werr == nil {
			g.workspace = wd
		}
	}
	if abs, aerr := filepath.Abs(g.workspace); aerr == nil {
		g.workspace = abs
	}
	env, err := openCLI(context.Background(), g.configPath, g.dataDir)
	if err != nil {
		sessionsPrintf(stderr, "kanban: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer env.db.Close() //nolint:errcheck // best-effort close of the CLI holder
	store := kanban.New(env.db, g.workspace)
	inv := &kanban.Invocation{Store: store, Board: g.board, Actor: g.actor}
	v, err := inv.Run(context.Background(), sub, subArgs)
	if err != nil {
		return kanbanError(sub, err, stderr)
	}
	return kanbanOutput(v, g.json, stdout)
}

func kanbanError(sub string, err error, stderr io.Writer) int {
	sessionsPrintf(stderr, "kanban %s: %s\n", sub, cleanLine(err.Error()))
	if errors.Is(err, kanban.ErrInvalid) {
		return 2
	}
	return 1
}

func kanbanOutput(v any, asJSON bool, stdout io.Writer) int {
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return 1
		}
		return 0
	}
	if text := kanbanSummary(v); text != "" {
		sessionsPrintf(stdout, "%s\n", text)
	}
	return 0
}

// scanKanbanGlobals removes the shared flags from args. A missing value or an
// unknown leading dash is a usage error (exit 2). Subcommand flags are left
// in the returned slice.
func scanKanbanGlobals(args []string, g *kanbanGlobal) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--json" {
			g.json = true
			continue
		}
		name, val, hasVal := strings.Cut(a, "=")
		set, ok := kanbanGlobalFlag(name)
		if !ok {
			out = append(out, a)
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s needs a value", name)
			}
			i++
			val = args[i]
		}
		set(g, val)
	}
	return out, nil
}

func kanbanGlobalFlag(name string) (func(*kanbanGlobal, string), bool) {
	switch name {
	case "--board":
		return func(g *kanbanGlobal, v string) { g.board = v }, true
	case "--actor":
		return func(g *kanbanGlobal, v string) { g.actor = v }, true
	case "--workspace":
		return func(g *kanbanGlobal, v string) { g.workspace = v }, true
	case "--config":
		return func(g *kanbanGlobal, v string) { g.configPath = v }, true
	case "--data-dir":
		return func(g *kanbanGlobal, v string) { g.dataDir = v }, true
	}
	return nil, false
}

func kanbanUsage(w io.Writer) {
	sessionsPrintf(w, `usage: damnbot kanban <subcommand> [flags]

Board: init, boards, create, swarm, list, show, stats, context, assignees
Tasks: assign, set-model, reclaim, reassign, claim, edit, block, schedule,
       unblock, request-review, request-changes, reopen-review, promote,
       archive, complete, specify, heartbeat, link, unlink, dependencies
History: tail, log, runs, watch, diagnostics, repair, gc
Files: attach, attach-url, attachments, attach-rm
Dispatch: dispatch, daemon, decompose
Notify: notify-subscribe, notify-list, notify-unsubscribe

Global flags: --board <name> --actor <id> --workspace <dir> --json
              --config <file> --data-dir <dir>

The 45 canonical subcommands are listed by `+"`damnbot kanban --help`"+`.
`)
}

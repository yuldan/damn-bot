package tui

import "strings"

// slashKind is one SPEC §13.1 slash command implemented by A4. Other slashes
// are rejected with a reason instead of being sent to the model.
type slashKind int

const (
	slashUnknown slashKind = iota
	slashNew
	slashFork
	slashRewind
	slashUndo
	slashPlan
	slashTheme
	slashModel
	slashBudget
	slashModules
	slashContext
	slashReload
	slashCompact
	slashExport
	slashDiff
	slashBtw
	slashInit
	slashReview
)

// slashCommand is one parsed single-line slash invocation.
type slashCommand struct {
	kind slashKind
	name string   // as typed, for the unknown-command notice
	args []string // already split; dispatch validates the count
}

// parseSlash recognizes a single-line command. A multi-line prompt keeps its
// leading slash (prompts can legitimately start with one); an empty or
// whitespace-only line is not a command either.
func parseSlash(text string) (slashCommand, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.Contains(text, "\n") {
		return slashCommand{}, false
	}
	fields := strings.Fields(text)
	cmd := slashCommand{name: fields[0]}
	kinds := map[string]slashKind{
		"/new": slashNew, "/fork": slashFork, "/rewind": slashRewind, "/undo": slashUndo,
		"/plan": slashPlan, "/theme": slashTheme, "/model": slashModel, "/budget": slashBudget,
		"/modules": slashModules, "/context": slashContext, "/reload": slashReload,
		"/compact": slashCompact, "/export": slashExport, "/diff": slashDiff, "/btw": slashBtw,
		"/init": slashInit, "/review": slashReview,
	}
	cmd.kind = kinds[fields[0]]
	cmd.args = fields[1:]
	return cmd, true
}

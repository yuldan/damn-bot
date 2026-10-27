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
	switch fields[0] {
	case "/new":
		cmd.kind = slashNew
	case "/fork":
		cmd.kind = slashFork
	case "/rewind":
		cmd.kind = slashRewind
	case "/undo":
		cmd.kind = slashUndo
	case "/plan":
		cmd.kind = slashPlan
	case "/theme":
		cmd.kind = slashTheme
	case "/model":
		cmd.kind = slashModel
	case "/budget":
		cmd.kind = slashBudget
	case "/modules":
		cmd.kind = slashModules
	case "/context":
		cmd.kind = slashContext
	case "/reload":
		cmd.kind = slashReload
	default:
		cmd.kind = slashUnknown
	}
	cmd.args = fields[1:]
	return cmd, true
}

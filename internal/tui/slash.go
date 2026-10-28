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
	slashQueue
	slashSteer
	slashBusy
)

// slashCommand is one parsed single-line slash invocation.
type slashCommand struct {
	kind slashKind
	name string   // as typed, for the unknown-command notice
	args []string // already split; dispatch validates the count
}

// slashItem is one registry row shared by the parser and the ctrl+p palette.
type slashItem struct {
	name, desc string
	kind       slashKind
}

func slashCatalog() []slashItem {
	return []slashItem{
		{"/new", "new session", slashNew},
		{"/fork", "fork at head", slashFork},
		{"/rewind", "rewind at seq|turn:ID", slashRewind},
		{"/undo", "undo last action", slashUndo},
		{"/plan", "toggle plan mode", slashPlan},
		{"/theme", "show or set theme", slashTheme},
		{"/model", "list models (switch needs restart)", slashModel},
		{"/budget", "turn/session/day budget", slashBudget},
		{"/modules", "module registry status", slashModules},
		{"/context", "context window panel", slashContext},
		{"/reload", "reload config snapshot", slashReload},
		{"/compact", "run compaction", slashCompact},
		{"/export", "write transcript to path", slashExport},
		{"/diff", "journal working diff", slashDiff},
		{"/btw", "side question (refused)", slashBtw},
		{"/init", "write AGENTS.md template", slashInit},
		{"/review", "pending review queue", slashReview},
		{"/queue", "queue prompt/list/edit/rm/move/clear", slashQueue},
		{"/steer", "inject mid-turn (refused: no seam)", slashSteer},
		{"/busy", "busy input mode (queue)", slashBusy},
	}
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
	for _, it := range slashCatalog() {
		if it.name == fields[0] {
			cmd.kind = it.kind
		}
	}
	cmd.args = fields[1:]
	return cmd, true
}

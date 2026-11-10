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
	slashBrowser
	slashVoice
	slashWake

	slashPaste
	slashImage
	slashF8
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
		{"/browser", "browser status/connect/close/engine", slashBrowser},
		{"/voice", "voice mode on|off|tts|status", slashVoice},
		{"/wake", "wake listener on|off|status", slashWake},

		{"/paste", "attach an image from the clipboard", slashPaste},
		{"/image", "attach a local image for the next prompt", slashImage},

		{"/goal", "standing goal text|status|clear", slashF8},
		{"/subgoal", "goal criteria text|list|remove", slashF8},
		{"/heartbeat", "idle beat status|pause|resume|clear|set", slashF8},
		{"/hb", "alias of /heartbeat", slashF8},
		{"/loop", "recurring prompt interval text [--times N]", slashF8},
		{"/proactive", "alias of /loop", slashF8},
		{"/personality", "persona list|<name>", slashF8},
		{"/suggestions", "suggestions accept|dismiss|catalog|clear", slashF8},
		{"/suggest", "alias of /suggestions", slashF8},
		{"/blueprint", "blueprint catalog|<name> slot=value", slashF8},
		{"/bp", "alias of /blueprint", slashF8},
		{"/insights", "usage analytics for the last N days", slashF8},
		{"/journey", "skill timeline list|delete|edit", slashF8},
		{"/learning", "alias of /journey", slashF8},
		{"/memory-graph", "alias of /journey", slashF8},
		{"/pet", "mascot list|toggle|select|show|scale|off", slashF8},
		{"/hatch", "generate a pet from a description", slashF8},
		{"/generate-pet", "alias of /hatch", slashF8},
		{"/debug", "local one-use debug share and delete", slashF8},
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

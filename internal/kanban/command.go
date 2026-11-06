package kanban

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Invocation is the shared command surface of `damnbot kanban` (CLI-029) and
// `/kanban` (SLASH-080). The same operations back both, so the chat command
// cannot drift from the CLI.
type Invocation struct {
	Store      *Store
	Board      string
	Actor      string
	Runner     Runner
	Decomposer Decomposer
}

// Mutating reports whether a subcommand writes board state. The chat surface
// refuses these for every role but owner.
func Mutating(sub string) bool {
	switch sub {
	case "boards", "list", "ls", "show", "attachments", "tail", "watch", "stats", "diag", "diagnostics",
		"log", "runs", "assignees", "context", "notify-list":
		return false
	}
	return true
}

// Subcommands returns the closed 45-command set of CLI-029 in help order.
func Subcommands() []string {
	return []string{
		"init", "boards", "create", "swarm", "list", "show", "assign", "set-model", "reclaim", "reassign",
		"diagnostics", "link", "unlink", "claim", "comment", "attach", "attachments", "attach-rm", "complete",
		"edit", "block", "schedule", "unblock", "request-review", "request-changes", "reopen-review", "promote",
		"archive", "tail", "dispatch", "daemon", "watch", "stats", "notify-subscribe", "notify-list",
		"notify-unsubscribe", "log", "runs", "heartbeat", "assignees", "context", "specify", "decompose", "gc", "repair",
	}
}

type handler func(*Invocation, context.Context, string, []string) (any, error)

// Run executes one subcommand. Unknown names are ErrInvalid (CLI maps that to
// exit 2); a named command's domain failure is returned unchanged.
func (iv *Invocation) Run(ctx context.Context, sub string, args []string) (any, error) {
	if iv.Store == nil {
		return nil, fmt.Errorf("%w: store", ErrNotConfigured)
	}
	if iv.Board == "" {
		iv.Board = "default"
	}
	h, ok := commandHandlers()[sub]
	if !ok {
		return nil, fmt.Errorf("%w: unknown subcommand %q", ErrInvalid, sub)
	}
	return h(iv, ctx, sub, args)
}

func commandHandlers() map[string]handler { return nil }

// Summary renders a short, renderer-safe text answer for the chat surface.
func Summary(v any) string {
	if s := summaryCard(v); s != "" {
		return s
	}
	if s := summaryMeta(v); s != "" {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func summaryCard(v any) string {
	switch x := v.(type) {
	case Board:
		return x.Name + "\t" + x.ID
	case []Board:
		return lines(len(x), func(i int) string { return x[i].Name + "\t" + x[i].ID })
	case *Task:
		return taskLine(x)
	case Task:
		return taskLine(&x)
	case []Task:
		return lines(len(x), func(i int) string { return taskLine(&x[i]) })
	case []*Task:
		return lines(len(x), func(i int) string { return taskLine(x[i]) })
	}
	return ""
}

func summaryMeta(v any) string {
	switch x := v.(type) {
	case Event:
		return eventLine(&x)
	case []Event:
		return lines(len(x), func(i int) string { return eventLine(&x[i]) })
	case Attachment:
		return x.Name + "\t" + strconv.FormatInt(x.Size, 10)
	case []Attachment:
		return lines(len(x), func(i int) string { return x[i].Name + "\t" + strconv.FormatInt(x[i].Size, 10) })
	case Stats:
		return "open=" + strconv.Itoa(x.Open) + " done=" + strconv.Itoa(x.Counts[StatusDone]) +
			" blocked=" + strconv.Itoa(x.Counts[StatusBlocked]) + " runs=" + strconv.Itoa(x.Runs)
	case BoardContext:
		return lines(len(x.Tasks), func(i int) string {
			return x.Tasks[i].ID + "\t" + string(x.Tasks[i].Status) + "\t" + x.Tasks[i].Title
		})
	case []Run:
		return lines(len(x), func(i int) string { return x[i].ID + "\t" + x[i].Outcome })
	case []AssigneeStat:
		return lines(len(x), func(i int) string { return x[i].Assignee + "\t" + strconv.Itoa(x[i].Count) })
	}
	return ""
}

func lines(n int, f func(int) string) string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, f(i))
	}
	return strings.Join(out, "\n")
}

func taskLine(t *Task) string {
	if t == nil {
		return ""
	}
	return t.ID + "\t" + string(t.Status) + "\t" + t.Title
}

func eventLine(e *Event) string {
	return strconv.FormatInt(e.ID, 10) + "\t" + e.Kind + "\t" + e.Actor + "\t" + string(e.Payload)
}

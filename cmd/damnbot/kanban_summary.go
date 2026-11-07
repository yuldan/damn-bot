//go:build !minimal

package main

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/kanban"
)

// kanbanSummary renders a short, renderer-safe text answer for the CLI and
// chat surfaces. JSON encoding is the fallback for structs with no compact
// one-line form.
func kanbanSummary(v any) string {
	if s := kanbanCard(v); s != "" {
		return s
	}
	if s := kanbanMeta(v); s != "" {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func kanbanCard(v any) string {
	switch x := v.(type) {
	case kanban.Board:
		return x.Name + "\t" + x.ID
	case []kanban.Board:
		return kanbanLines(len(x), func(i int) string { return x[i].Name + "\t" + x[i].ID })
	case *kanban.Task:
		return kanbanTaskLine(x)
	case kanban.Task:
		return kanbanTaskLine(&x)
	case []kanban.Task:
		return kanbanLines(len(x), func(i int) string { return kanbanTaskLine(&x[i]) })
	case []*kanban.Task:
		return kanbanLines(len(x), func(i int) string { return kanbanTaskLine(x[i]) })
	}
	return ""
}

func kanbanMeta(v any) string {
	switch x := v.(type) {
	case kanban.Event:
		return kanbanEventLine(&x)
	case []kanban.Event:
		return kanbanLines(len(x), func(i int) string { return kanbanEventLine(&x[i]) })
	case kanban.Attachment:
		return x.Name + "\t" + strconv.FormatInt(x.Size, 10)
	case []kanban.Attachment:
		return kanbanLines(len(x), func(i int) string { return x[i].Name + "\t" + strconv.FormatInt(x[i].Size, 10) })
	case kanban.Stats:
		return "open=" + strconv.Itoa(x.Open) + " done=" + strconv.Itoa(x.Counts[kanban.StatusDone]) +
			" blocked=" + strconv.Itoa(x.Counts[kanban.StatusBlocked]) + " runs=" + strconv.Itoa(x.Runs)
	case kanban.BoardContext:
		return kanbanLines(len(x.Tasks), func(i int) string {
			return x.Tasks[i].ID + "\t" + string(x.Tasks[i].Status) + "\t" + x.Tasks[i].Title
		})
	case []kanban.Run:
		return kanbanLines(len(x), func(i int) string { return x[i].ID + "\t" + x[i].Outcome })
	case []kanban.AssigneeStat:
		return kanbanLines(len(x), func(i int) string { return x[i].Assignee + "\t" + strconv.Itoa(x[i].Count) })
	}
	return ""
}

func kanbanLines(n int, f func(int) string) string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, f(i))
	}
	return strings.Join(out, "\n")
}

func kanbanTaskLine(t *kanban.Task) string {
	if t == nil {
		return ""
	}
	return t.ID + "\t" + string(t.Status) + "\t" + t.Title
}

func kanbanEventLine(e *kanban.Event) string {
	return strconv.FormatInt(e.ID, 10) + "\t" + e.Kind + "\t" + e.Actor + "\t" + string(e.Payload)
}

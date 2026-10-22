package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
)

// recapSnippet bounds one quoted line in the resume recap.
const recapSnippet = 120

// recapInfo is the header of the resume recap.
type recapInfo struct {
	title, origin, cwd, model string
	events                    int64
}

// loadRecap reads one visible session's metadata, title and event count.
func loadRecap(ctx context.Context, db *ctxdb.Store, sid string) (recapInfo, error) {
	var in recapInfo
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		if err := tx.Get(`SELECT s.origin, s.cwd, s.model FROM sessions s
			WHERE s.id = ?1 AND NOT EXISTS (SELECT 1 FROM session_labels l
			  WHERE l.session_id = s.id AND l.key = 'purge_state')`,
			[]any{sid}, &in.origin, &in.cwd, &in.model); err != nil {
			return fmt.Errorf("session row: %w", err)
		}
		if err := tx.Get(`SELECT value FROM session_labels WHERE session_id = ?1 AND key = 'title'
			ORDER BY id DESC LIMIT 1`, []any{sid}, &in.title); err != nil && !errors.Is(err, ctxdb.ErrNotFound) {
			return fmt.Errorf("title label: %w", err)
		}
		if err := tx.Get(`SELECT count(*) FROM events WHERE session_id = ?1`, []any{sid}, &in.events); err != nil {
			return fmt.Errorf("event count: %w", err)
		}
		return nil
	})
	if err != nil {
		return recapInfo{}, fmt.Errorf("recap session %s: %w", sid, err)
	}
	return in, nil
}

// printRecap writes the resume header and the last exchange to stderr, so
// --output json stays machine-readable. Every field is redacted and bounded.
func printRecap(ctx context.Context, db *ctxdb.Store, sid string, red *redact.Redactor, w io.Writer) error {
	in, err := loadRecap(ctx, db, sid)
	if err != nil {
		return err
	}
	label := sid
	if in.title != "" {
		label = fmt.Sprintf("%s (%s)", sid, in.title)
	}
	recapPrintf(w, "resumed %s: %d events, model %s, cwd %s\n",
		clip(red, label), in.events, clip(red, in.model), clip(red, in.cwd))
	evs, err := db.HistoryTail(ctx, sid, 200)
	if err != nil {
		return fmt.Errorf("recap history: %w", err)
	}
	user, assistant := lastSnippets(evs)
	if user != "" {
		recapPrintf(w, "last user: %s\n", clip(red, user))
	}
	if assistant != "" {
		recapPrintf(w, "last assistant: %s\n", clip(red, assistant))
	}
	return nil
}

// lastSnippets returns the newest non-empty user and assistant contents.
func lastSnippets(evs []ctxdb.Event) (user, assistant string) {
	for i := len(evs) - 1; i >= 0 && (user == "" || assistant == ""); i-- {
		e := &evs[i]
		switch {
		case e.Kind == "user_message" && user == "":
			user = snippet(e.Content)
		case e.Kind == "assistant_message" && assistant == "":
			assistant = snippet(e.Content)
		}
	}
	return user, assistant
}

// retryPrompt resolves --retry: the last user message, plus a warning when
// its turn was excluded after a failure that executed side effects (#211).
// Repeating such a turn re-runs those effects, so they are named on stderr
// before the request is sent.
func retryPrompt(ctx context.Context, db *ctxdb.Store, sid string, red *redact.Redactor, w io.Writer) (string, error) {
	evs, err := db.HistoryTail(ctx, sid, 100000)
	if err != nil {
		return "", fmt.Errorf("retry history: %w", err)
	}
	excluded := contextExclusions(evs)
	var last *ctxdb.Event
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == "user_message" && evs[i].Content != "" {
			last = &evs[i]
			break
		}
	}
	if last == nil {
		return "", errors.New("no previous user message to retry")
	}
	if last.TurnID == "" || !excluded[last.TurnID] {
		return last.Content, nil
	}
	recapPrintf(w, "retry: turn %s was excluded after a failure\n", clip(red, last.TurnID))
	notice := failedTurnEffects(evs, last.TurnID)
	executed := dedupe(append(mutatingResults(evs, last.TurnID), notice.effects...))
	if len(executed) > 0 {
		recapPrintf(w, "retry: side effects already executed in that turn: %s; resending the message may repeat them\n",
			clip(red, strings.Join(executed, ", ")))
	}
	if len(notice.uncertain) > 0 {
		recapPrintf(w, "retry: side effects with unknown outcome in that turn: %s; resending the message may run them again\n",
			clip(red, strings.Join(notice.uncertain, ", ")))
	}
	return last.Content, nil
}

// turnEffects is a failed_turn_effects notice (SEC11-3): effects that ran and
// calls whose outcome is unknown after a crash.
type turnEffects struct {
	effects, uncertain []string
}

// failedTurnEffects finds the turnless notice carrying that turn's recorded
// effects. The notice has no turn_id column, so it is matched by payload.
func failedTurnEffects(evs []ctxdb.Event, turn string) turnEffects {
	for i := range evs {
		e := &evs[i]
		if e.Kind != "system_notice" {
			continue
		}
		var p struct {
			Kind      string   `json:"kind"`
			TurnID    string   `json:"turn_id"`
			Effects   []string `json:"effects"`
			Uncertain []string `json:"uncertain"`
		}
		if json.Unmarshal([]byte(e.Payload), &p) != nil || p.Kind != "failed_turn_effects" || p.TurnID != turn {
			continue
		}
		return turnEffects{effects: p.Effects, uncertain: p.Uncertain}
	}
	return turnEffects{}
}

// dedupe preserves order and drops empty/duplicate labels.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// contextExclusions collects the turn ids marked by context_exclude (ADR-134).
func contextExclusions(evs []ctxdb.Event) map[string]bool {
	out := map[string]bool{}
	for i := range evs {
		if evs[i].Kind == "context_exclude" && evs[i].TurnID != "" {
			out[evs[i].TurnID] = true
		}
	}
	return out
}

// mutatingResults names successful tool calls of one turn whose dispatch
// effect is neither read nor think. Call ids are included because arguments
// are not stored in the journal.
func mutatingResults(evs []ctxdb.Event, turn string) []string {
	effects := map[string]string{}
	for i := range evs {
		e := &evs[i]
		if e.Kind != "tool_dispatch" || e.TurnID != turn || e.ToolCallID == "" {
			continue
		}
		var p struct {
			Effect string `json:"effect"`
		}
		if json.Unmarshal([]byte(e.Payload), &p) == nil {
			effects[e.ToolCallID] = p.Effect
		}
	}
	seen := map[string]bool{}
	var out []string
	for i := range evs {
		e := &evs[i]
		if e.Kind != "tool_result" || e.TurnID != turn || e.ToolCallID == "" {
			continue
		}
		effect := effects[e.ToolCallID]
		if effect == "read" || effect == "think" || !resultOK(e.Payload) {
			continue
		}
		label := recapArg(e.ToolName) + "(" + recapArg(e.ToolCallID) + ")"
		if seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}

// resultOK reads the dispatcher's {"ok":bool} tool_result payload.
func resultOK(payload string) bool {
	var p struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal([]byte(payload), &p) == nil && p.OK
}

// snippet collapses one message to a single bounded line of its first
// non-empty line.
func snippet(s string) string {
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// clip redacts and bounds one recap field.
func clip(red *redact.Redactor, s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if red != nil {
		s = red.String(s)
	}
	return clipRunes(s, recapSnippet)
}

// clipRunes truncates on a rune boundary so terminals never receive half a
// UTF-8 sequence.
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// recapPrintf writes a stderr note; CLI output errors are not actionable.
func recapPrintf(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}

// recapArg strips control characters and bounds one echoed argument.
func recapArg(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return clipRunes(s, 80)
}

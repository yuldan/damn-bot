// `damnbot sessions why <turn-id>` (SPEC §10, §4.2): the decision chain of one
// turn, rendered from persisted events. The turn id is looked up across
// sessions (TurnSessions), so --session is an optional filter, not a
// requirement. Only events are read: turn_journal rows are deleted when a turn
// ends, so they must never be the source of truth here.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

type whyArgs struct {
	dataDir, session, turnID string
}

func parseWhyArgs(args []string, stderr io.Writer) (whyArgs, error) {
	var a whyArgs
	fs := flag.NewFlagSet("sessions why", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.session, "session", "", "only this session id (optional)")
	fs.StringVar(&a.dataDir, "data-dir", "", "data directory (default: SPEC §11)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return whyArgs{}, err
	}
	if len(pos) != 1 {
		return whyArgs{}, fmt.Errorf("one turn id expected, got %d", len(pos))
	}
	a.turnID = pos[0]
	return a, nil
}

// sessionsWhy prints one block per owning session: a header and one line per
// event of the turn, in seq order.
func sessionsWhy(args []string, stdout, stderr io.Writer) int {
	a, err := parseWhyArgs(args, stderr)
	if err != nil {
		return usageError(stderr, "sessions why", err)
	}
	ctx := context.Background()
	db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "why", derr, a.session)
	}
	defer closeSessionsDB(ctx, db, "sessions why", stderr)
	owners, oerr := db.TurnSessions(ctx, a.turnID)
	if errors.Is(oerr, ctxdb.ErrNotFound) {
		sessionsPrintf(stderr, "sessions why: turn %s not found\n", cleanArg(a.turnID))
		return 1
	}
	if oerr != nil {
		return sessionsFail(stderr, "why", oerr, a.turnID)
	}
	if a.session != "" {
		if !slices.Contains(owners, a.session) {
			sessionsPrintf(stderr, "sessions why: turn %s is not in session %s\n", cleanArg(a.turnID), cleanArg(a.session))
			return 1
		}
		owners = []string{a.session}
	}
	for _, sid := range owners {
		evs, herr := whyEvents(ctx, db, sid, a.turnID)
		if herr != nil {
			return sessionsFail(stderr, "why", herr, sid)
		}
		if len(evs) == 0 {
			sessionsPrintf(stderr, "sessions why: turn %s has no events in session %s\n", cleanArg(a.turnID), cleanArg(sid))
			return 1
		}
		sessionsPrintf(stdout, "session %s\tturn %s\tevents %d\n", cleanArg(sid), cleanArg(a.turnID), len(evs))
		for i := range evs {
			sessionsPrintf(stdout, "%s\n", whyLine(&evs[i]))
		}
	}
	return 0
}

// whyEvents reads the turn's seq window with History (raw, excluding nothing)
// and keeps exactly the events inside TurnRange. The window starts one seq
// before the turn: History returns rows with seq > fromSeq.
func whyEvents(ctx context.Context, db *ctxdb.Store, sid, turnID string) ([]ctxdb.Event, error) {
	first, last, err := db.TurnRange(ctx, sid, turnID)
	if err != nil {
		return nil, fmt.Errorf("turn range: %w", err)
	}
	from := max(first-1, 0)
	evs, err := db.History(ctx, sid, from, ctxdb.WithHistoryRaw(), ctxdb.WithHistoryExcluded())
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	out := make([]ctxdb.Event, 0, len(evs))
	for i := range evs {
		if evs[i].Seq >= first && evs[i].Seq <= last {
			out = append(out, evs[i])
		}
	}
	return out, nil
}

// whyPayload decodes an event payload; malformed payloads render as empty
// rather than failing the chain (the event still explains itself).
func whyPayload(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

// whyLine renders one event as `seq N<TAB>kind[<TAB>tool]<TAB>facts`. Only
// fields defined by SPEC/loop payloads are shown; everything is sanitized for
// the terminal. Free-text kinds keep their content, everything else comes from
// the payload vocabulary in whyFacts.
func whyLine(ev *ctxdb.Event) string {
	line := fmt.Sprintf("seq %d\t%s", ev.Seq, cleanArg(ev.Kind))
	if ev.ToolName != "" {
		line += "\t" + cleanArg(ev.ToolName)
	}
	switch ev.Kind {
	case "user_message", "assistant_message", "error", "stream_aborted", "crash_recovered", "system_notice":
		return line + "\t" + whyText(ev.Content)
	}
	if parts := whyFacts(ev.Kind, whyPayload(ev.Payload)); len(parts) > 0 {
		line += "\t" + strings.Join(parts, " ")
	}
	return line
}

// whyFacts maps one event kind to its documented payload fields.
func whyFacts(kind string, f map[string]any) []string {
	var parts []string
	add := func(keys ...string) {
		for _, k := range keys {
			if v, ok := f[k]; ok {
				parts = append(parts, k+"="+whyValue(v))
			}
		}
	}
	switch kind {
	case "turn_start":
		add("plan_mode")
		if t, ok := f["text"].(string); ok && t != "" {
			parts = append(parts, "text="+whyText(t))
		}
	case "llm_call":
		add("model", "finish_reason", "fallback_from", "attempts", "input_tokens", "cached_input_tokens", "output_tokens", "cost_usd")
	case "provider_fallback":
		add("from", "to")
	case "budget_warning", "budget_exhausted":
		add("code", "kind", "message")
	case "tool_rejected":
		add("tool", "code", "message")
	case "approval":
		add("tool", "decision")
	case "tool_dispatch":
		add("effect")
	case "tool_result":
		add("status", "tainted")
	case "turn_end":
		add("stop_reason", "detail", "iterations", "input_tokens", "output_tokens", "cost_usd", "tainted", "error")
	}
	return parts
}

// whyText bounds one free-text field to a single terminal line.
func whyText(s string) string {
	s = cleanLine(s)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// whyValue renders one payload scalar (string, number, bool) safely.
func whyValue(v any) string {
	switch t := v.(type) {
	case string:
		return whyText(t)
	default:
		return whyText(fmt.Sprint(v))
	}
}

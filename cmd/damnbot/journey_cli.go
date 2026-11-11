//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/memoryproviders"
	"github.com/damnbot/damnbot/internal/skills"
)

// f8JourneyEntry is one deterministic timeline row (CLI-051, MEM-035).
type f8JourneyEntry struct {
	Key    string `json:"key"`
	At     int64  `json:"at"`
	Action string `json:"action"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Note   string `json:"note,omitempty"`
}

// f8JourneyState is the <data_dir>/journey.json overlay: tombstones and the
// operator's notes. The ledger itself is append-only and never rewritten.
type f8JourneyState struct {
	Deleted []string          `json:"deleted"`
	Notes   map[string]string `json:"notes"`
}

// runJourney implements `damnbot journey` (CLI-051).
func runJourney(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("journey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory (default: profile data dir)")
	if err := fs.Parse(args); err != nil {
		return f8ExitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		journeyUsage(stderr)
		return f8ExitUsage
	}
	dir, err := f8Dir(*dataDir)
	if err != nil {
		return f8CmdFail(stderr, "journey", err)
	}
	st := f8JourneyState{Notes: map[string]string{}}
	if lerr := f8Load(f8StorePath(dir, "journey.json"), &st); lerr != nil {
		return f8CmdFail(stderr, "journey", lerr)
	}
	if st.Notes == nil {
		st.Notes = map[string]string{}
	}
	changed, err := journeyCommand(rest, dir, &st, stdout, stderr)
	if err != nil {
		if errors.Is(err, errF8Usage) {
			return f8ExitUsage
		}
		return f8CmdFail(stderr, "journey", err)
	}
	if changed {
		if err := f8Save(f8StorePath(dir, "journey.json"), &st); err != nil {
			return f8CmdFail(stderr, "journey", err)
		}
	}
	return f8ExitOK
}

func journeyUsage(w io.Writer) {
	say(w, "usage: damnbot journey list [--limit N] [--json]\n")
	say(w, "       damnbot journey delete <key>\n")
	say(w, "       damnbot journey edit <key> --note TEXT\n")
}

func journeyCommand(rest []string, dir string, st *f8JourneyState, stdout, stderr io.Writer) (bool, error) {
	switch rest[0] {
	case "list":
		return false, journeyList(rest[1:], dir, st, stdout, stderr)
	case "delete":
		return journeyDelete(rest[1:], st, stdout, stderr)
	case "edit":
		return journeyEdit(context.Background(), rest[1:], dir, st, stdout, stderr)
	default:
		say(stderr, "journey: unknown subcommand %q\n", cleanArg(rest[0]))
		journeyUsage(stderr)
		return false, errF8Usage
	}
}

// journeyEntries reads the skills ledger and the local memory table, folds
// both through the shared builder (dedupe, chronology, redaction), then applies
// the operator overlay (tombstones and notes).
func journeyEntries(ctx context.Context, dir string, st *f8JourneyState) ([]f8JourneyEntry, error) {
	events, err := journeyEvents(ctx, dir)
	if err != nil {
		return nil, err
	}
	built, err := memoryproviders.BuildJourney(events, memoryproviders.MaxJourneyEntries)
	if err != nil {
		return nil, fmt.Errorf("journey builder: %w", err)
	}
	out := make([]f8JourneyEntry, 0, len(built))
	for _, e := range built {
		key := journeyEntryKey(&e)
		if slices.Contains(st.Deleted, key) {
			continue
		}
		src := e.Source
		if strings.TrimSpace(src) == "" {
			src = f8JourneyRedactedSource
		} else {
			src = f8Redact(src)
		}
		out = append(out, f8JourneyEntry{
			Key: key, At: e.At.Unix(), Action: journeyAction(&e), Name: e.Title,
			Source: src, Note: st.Notes[key],
		})
	}
	return out, nil
}

// f8JourneyRedactedSource marks a ledger row without a recorded source.
const f8JourneyRedactedSource = "[REDACTED:source]"

// journeyEntryKey keeps the historical skill key (so existing tombstones and
// notes survive) and namespaces memory rows by their local id.
func journeyEntryKey(e *memoryproviders.JourneyEntry) string {
	if e.Kind == memoryproviders.JourneyMemory {
		return "memory:" + e.ID
	}
	return e.ID
}

func journeyEvents(ctx context.Context, dir string) ([]memoryproviders.JourneyEvent, error) {
	ledger, err := skills.ReadLedger(dir)
	if err != nil {
		return nil, fmt.Errorf("journey ledger: %w", err)
	}
	events := make([]memoryproviders.JourneyEvent, 0, len(ledger))
	for _, e := range ledger {
		src := e.Source
		if strings.TrimSpace(src) == "" {
			// Keep the historical CLI marker while still letting the builder
			// dedupe and order the row.
			src = f8JourneyRedactedSource
		}
		events = append(events, memoryproviders.JourneyEvent{
			At: time.Unix(e.At, 0), Kind: memoryproviders.JourneySkill,
			ID: journeyKey(e.At, e.Name, e.Action), Title: e.Name, Source: src,
		})
	}
	return append(events, journeyMemoryEvents(ctx, dir)...), nil
}

// journeyMemoryEvents merges locally stored memories (kind='memory'). The DB
// is best-effort: a missing or unreadable store leaves skills-only output.
func journeyMemoryEvents(ctx context.Context, dir string) []memoryproviders.JourneyEvent {
	if _, err := os.Stat(ctxdb.DBPath(dir)); err != nil {
		return nil
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		return nil
	}
	var (
		id, at          int64
		content, origin string
		events          []memoryproviders.JourneyEvent
	)
	query := `SELECT id, content, origin, created_at FROM memory WHERE kind = 'memory' ORDER BY created_at, id`
	err = db.Rows(ctx, query, nil, []any{&id, &content, &origin, &at}, func() error {
		if strings.TrimSpace(content) == "" {
			return nil
		}
		events = append(events, memoryproviders.JourneyEvent{
			At: time.Unix(at, 0), Kind: memoryproviders.JourneyMemory,
			ID: strconv.FormatInt(id, 10), Title: content, Source: origin,
		})
		return nil
	})
	if cerr := db.CloseContext(ctx); cerr != nil {
		return nil
	}
	if err != nil {
		return nil
	}
	return events
}

func journeyAction(e *memoryproviders.JourneyEntry) string {
	if e.Kind == memoryproviders.JourneyMemory {
		return "remembered"
	}
	if parts := strings.SplitN(e.ID, ":", 3); len(parts) == 3 {
		return parts[1]
	}
	return "skill"
}

func journeyKey(at int64, name, action string) string {
	return strconv.FormatInt(at, 10) + ":" + action + ":" + name
}

func journeyList(args []string, dir string, st *f8JourneyState, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("journey list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 0, "newest N entries; 0 lists all")
	asJSON := fs.Bool("json", false, "JSON rows")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return errF8Usage
	}
	if len(pos) != 0 || *limit < 0 {
		journeyUsage(stderr)
		return errF8Usage
	}
	entries, err := journeyEntries(context.Background(), dir, st)
	if err != nil {
		return fmt.Errorf("journey ledger: %w", err)
	}
	if *limit > 0 && len(entries) > *limit {
		entries = entries[len(entries)-*limit:]
	}
	if *asJSON {
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fmt.Errorf("journey encode: %w", err)
		}
		say(stdout, "%s\n", b)
		return nil
	}
	for _, e := range entries {
		line := fmt.Sprintf("%s %s %s source=%s", time.Unix(e.At, 0).UTC().Format(time.RFC3339),
			cleanLine(e.Action), cleanLine(e.Name), cleanLine(e.Source))
		if e.Note != "" {
			line += " note=" + f8Redact(e.Note)
		}
		say(stdout, "%s\n", line)
	}
	return nil
}

func journeyDelete(args []string, st *f8JourneyState, stdout, stderr io.Writer) (bool, error) {
	if len(args) != 1 {
		say(stderr, "usage: damnbot journey delete <key>\n")
		return false, errF8Usage
	}
	if slices.Contains(st.Deleted, args[0]) {
		return false, nil
	}
	st.Deleted = append(st.Deleted, args[0])
	slices.Sort(st.Deleted)
	say(stdout, "deleted %s\n", args[0])
	return true, nil
}

func journeyEdit(ctx context.Context, args []string, dir string, st *f8JourneyState, stdout, stderr io.Writer) (bool, error) {
	fs := flag.NewFlagSet("journey edit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	note := fs.String("note", "", "operator note shown on the timeline row")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return false, errF8Usage
	}
	if len(pos) != 1 || strings.TrimSpace(*note) == "" {
		say(stderr, "usage: damnbot journey edit <key> --note TEXT\n")
		return false, errF8Usage
	}
	entries, err := journeyEntries(ctx, dir, st)
	if err != nil {
		return false, fmt.Errorf("journey ledger: %w", err)
	}
	if !slices.ContainsFunc(entries, func(e f8JourneyEntry) bool { return e.Key == pos[0] }) {
		return false, fmt.Errorf("journey entry %q not found", pos[0])
	}
	if st.Notes[pos[0]] == *note {
		return false, nil
	}
	st.Notes[pos[0]] = *note
	say(stdout, "edited %s\n", pos[0])
	return true, nil
}

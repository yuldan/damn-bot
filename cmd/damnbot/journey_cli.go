//go:build !minimal

package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

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
		return journeyEdit(rest[1:], dir, st, stdout, stderr)
	default:
		say(stderr, "journey: unknown subcommand %q\n", cleanArg(rest[0]))
		journeyUsage(stderr)
		return false, errF8Usage
	}
}

// journeyEntries reads the skills ledger and folds duplicates away.
func journeyEntries(dir string, st *f8JourneyState) ([]f8JourneyEntry, error) {
	ledger, err := skills.ReadLedger(dir)
	if err != nil {
		return nil, fmt.Errorf("journey ledger: %w", err)
	}
	seen := map[string]bool{}
	out := []f8JourneyEntry{}
	for _, e := range ledger {
		key := journeyKey(e.At, e.Name, e.Action)
		if seen[key] || slices.Contains(st.Deleted, key) {
			continue
		}
		seen[key] = true
		src := e.Source
		if strings.TrimSpace(src) == "" {
			src = "[REDACTED:source]"
		} else {
			src = f8Redact(src)
		}
		out = append(out, f8JourneyEntry{Key: key, At: e.At, Action: e.Action, Name: e.Name, Source: src, Note: st.Notes[key]})
	}
	slices.SortFunc(out, func(a, b f8JourneyEntry) int {
		if a.At != b.At {
			return cmp.Compare(a.At, b.At)
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out, nil
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
	entries, err := journeyEntries(dir, st)
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
		line := fmt.Sprintf("%s %s %s source=%s", time.Unix(e.At, 0).UTC().Format(time.RFC3339), e.Action, e.Name, e.Source)
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

func journeyEdit(args []string, dir string, st *f8JourneyState, stdout, stderr io.Writer) (bool, error) {
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
	entries, err := journeyEntries(dir, st)
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

//go:build !minimal

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/skills/hub"
)

type f8CuratorEvent struct {
	At     int64  `json:"at"`
	Op     string `json:"op"`
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type f8CuratorState struct {
	Paused   bool             `json:"paused,omitempty"`
	Pins     []string         `json:"pins"`
	Managed  []string         `json:"managed"`
	Archived []string         `json:"archived"`
	Events   []f8CuratorEvent `json:"events"`
	LastRun  int64            `json:"last_run,omitempty"`
}

func runCurator(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("curator", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return f8ExitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		curatorUsage(stderr)
		return f8ExitUsage
	}
	dir, err := f8Dir(*dataDir)
	if err != nil {
		return f8CmdFail(stderr, "curator", err)
	}
	st := f8CuratorState{}
	if lerr := f8Load(f8StorePath(dir, "curator.json"), &st); lerr != nil {
		return f8CmdFail(stderr, "curator", lerr)
	}
	changed, err := curatorCommand(rest, dir, &st, stdout, stderr)
	if err != nil {
		if errors.Is(err, errF8Usage) {
			return f8ExitUsage
		}
		return f8CmdFail(stderr, "curator", err)
	}
	if changed {
		if err := f8Save(f8StorePath(dir, "curator.json"), &st); err != nil {
			return f8CmdFail(stderr, "curator", err)
		}
	}
	return f8ExitOK
}

func curatorUsage(w io.Writer) {
	say(w, "usage: damnbot curator status|usage|run|pause|resume|pin|unpin|list-unmanaged|adopt|restore|list-archived|archive|prune|backup|rollback|ledger|purge --yes\n")
}

//nolint:gocognit,gocyclo // satu cabang switch per subcommand CLI-049/SLASH-079.
func curatorCommand(rest []string, dir string, st *f8CuratorState, stdout, stderr io.Writer) (bool, error) {
	switch rest[0] {
	case "status":
		say(stdout, "paused=%t pins=%d managed=%d archived=%d events=%d\n", st.Paused, len(st.Pins), len(st.Managed), len(st.Archived), len(st.Events))
		return false, nil
	case "usage":
		rep, err := hub.ReadUsage(dir)
		if err != nil {
			return false, fmt.Errorf("curator usage: %w", err)
		}
		slices.SortFunc(rep.Entries, func(a, b hub.UsageEntry) int { return strings.Compare(a.Name, b.Name) })
		for _, e := range rep.Entries {
			say(stdout, "%s indexed=%d quarantined=%d approved=%d rejected=%d other=%d last=%d\n", e.Name, e.Indexed, e.Quarantined, e.Approved, e.Rejected, e.Other, e.LastAt)
		}
		return false, nil
	case "run":
		return curatorRun(rest[1:], dir, st, stdout)
	case "pause", "resume":
		want := rest[0] == "pause"
		if st.Paused == want {
			return false, nil
		}
		st.Paused = want
		say(stdout, "curator %s\n", rest[0])
		return true, nil
	case "pin", "unpin", "adopt", "restore", "archive":
		return curatorSet(rest[0], arg(rest, 1), st, stdout)
	case "list-unmanaged":
		rep, err := hub.ReadUsage(dir)
		if err != nil {
			return false, fmt.Errorf("curator usage: %w", err)
		}
		names := []string{}
		for _, e := range rep.Entries {
			if !slices.Contains(st.Managed, e.Name) {
				names = append(names, e.Name)
			}
		}
		slices.Sort(names)
		for _, n := range names {
			say(stdout, "%s\n", n)
		}
		return false, nil
	case "list-archived":
		names := slices.Clone(st.Archived)
		slices.Sort(names)
		for _, n := range names {
			say(stdout, "%s\n", n)
		}
		return false, nil
	case "prune":
		if len(st.Archived) == 0 {
			return false, nil
		}
		st.Archived = nil
		say(stdout, "pruned archived entries\n")
		return true, nil
	case "backup", "rollback":
		return curatorTransfer(rest[0], rest[1:], dir, st, stdout, stderr)
	case "ledger":
		for _, e := range st.Events {
			say(stdout, "%d %s %s %s\n", e.At, e.Op, e.Name, e.Detail)
		}
		return false, nil
	case "purge":
		if len(rest) != 2 || rest[1] != "--yes" {
			return false, errF8Usage
		}
		empty := f8CuratorState{}
		if len(st.Pins)+len(st.Managed)+len(st.Archived)+len(st.Events) == 0 && !st.Paused && st.LastRun == 0 {
			return false, nil
		}
		*st = empty
		say(stdout, "purged curator state\n")
		return true, nil
	default:
		say(stderr, "curator: unknown subcommand %q\n", cleanArg(rest[0]))
		curatorUsage(stderr)
		return false, errF8Usage
	}
}

func curatorRun(args []string, dir string, st *f8CuratorState, stdout io.Writer) (bool, error) {
	budget := 5
	for i := 0; i < len(args); i++ {
		if args[i] == "--budget" && i+1 < len(args) {
			if _, err := fmt.Sscanf(args[i+1], "%d", &budget); err != nil {
				return false, errF8Usage
			}
			i++
		}
	}
	if st.Paused {
		return false, fmt.Errorf("curator is paused; run `damnbot curator resume` first")
	}
	rep, err := hub.ReadUsage(dir)
	if err != nil {
		return false, fmt.Errorf("curator usage: %w", err)
	}
	last := int64(0)
	for _, e := range rep.Entries {
		last = max(last, e.LastAt)
	}
	proposals := hub.Curate(rep, time.Unix(last, 0), hub.CuratorConfig{Now: time.Now, Budget: budget})
	if len(proposals) == 0 {
		say(stdout, "no proposals (idle gate or nothing stale)\n")
		return false, nil
	}
	now := time.Now().Unix()
	for _, p := range proposals {
		st.Events = append(st.Events, f8CuratorEvent{At: now, Op: "proposal:" + p.Action, Name: p.Name, Detail: p.Reason})
		say(stdout, "proposal %s %s: %s\n", p.Action, p.Name, p.Reason)
	}
	st.LastRun = now
	return true, nil
}

func curatorSet(sub, name string, st *f8CuratorState, stdout io.Writer) (bool, error) {
	if name == "" || len(name) > 128 {
		return false, errF8Usage
	}
	verb := map[string]string{"pin": "pinned", "unpin": "unpinned", "adopt": "adopted", "restore": "restored", "archive": "archived"}[sub]
	var list *[]string
	switch sub {
	case "pin":
		list = &st.Pins
	case "unpin":
		list = &st.Pins
	case "adopt":
		list = &st.Managed
	case "restore":
		list = &st.Archived
	default:
		list = &st.Archived
	}
	if sub == "unpin" || sub == "restore" {
		i := slices.Index(*list, name)
		if i < 0 {
			return false, nil
		}
		*list = slices.Delete(*list, i, i+1)
	} else {
		if slices.Contains(*list, name) {
			return false, nil
		}
		*list = append(*list, name)
		slices.Sort(*list)
	}
	say(stdout, "%s %s\n", verb, name)
	return true, nil
}

func curatorTransfer(sub string, args []string, dir string, st *f8CuratorState, stdout, _ io.Writer) (bool, error) {
	path := f8StorePath(dir, "curator-backup.json")
	flagName := "out"
	if sub == "rollback" {
		flagName = "from"
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--"+flagName && i+1 < len(args) {
			path = args[i+1]
			i++
		}
	}
	if sub == "backup" {
		if err := f8Save(path, st); err != nil {
			return false, err
		}
		say(stdout, "backup written %s\n", path)
		return false, nil
	}
	next := f8CuratorState{}
	if err := f8Load(path, &next); err != nil {
		return false, err
	}
	*st = next
	say(stdout, "rolled back %s\n", path)
	return true, nil
}

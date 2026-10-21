package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/journal"
	"github.com/damnbot/damnbot/internal/sandbox"
	"github.com/damnbot/damnbot/internal/tools"
)

// maxJournalImage is journalstore's per-image cap (1 GiB, JN2-19): a larger
// file is refused before its bytes are read, instead of allocating it.
const maxJournalImage = 1 << 30

// undoScope is the sandbox of a run, journaled with every action so undo
// rebuilds exactly it (REVIEW-F2-SEC5-1): canonical absolute roots and
// deny_paths with "~/" expanded at record time.
type undoScope struct {
	Roots     []string `json:"roots"`
	DenyPaths []string `json:"deny_paths"`
}

// journalScope canonicalizes the run's roots (absolute, symlinks resolved)
// and deny patterns into the journaled undo scope.
func journalScope(roots, deny []string, home string) (json.RawMessage, error) {
	sc := undoScope{DenyPaths: expandDeny(deny, home)}
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err == nil {
			abs, err = filepath.EvalSymlinks(abs)
		}
		if err != nil {
			return nil, fmt.Errorf("journal scope root %q: %w", r, err)
		}
		sc.Roots = append(sc.Roots, abs)
	}
	raw, err := json.Marshal(sc)
	if err != nil {
		return nil, fmt.Errorf("journal scope: %w", err)
	}
	return raw, nil
}

// expandDeny makes "~/" deny patterns absolute for home, so the journaled
// patterns do not depend on the home of the process that later undoes.
func expandDeny(deny []string, home string) []string {
	out := make([]string, 0, len(deny))
	for _, p := range deny {
		if rest, ok := strings.CutPrefix(p, "~/"); ok && home != "" {
			p = filepath.Join(home, rest)
		}
		out = append(out, p)
	}
	return out
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// journaledFileTools returns the eager file tools of session sid with every
// write journaled in db, under scope, before it lands.
func journaledFileTools(db *ctxdb.Store, sid string, scope json.RawMessage, engine *codetools.Engine, sb *sandbox.Sandbox) ([]tools.Tool, error) {
	store, err := journalstore.New(db, nil)
	if err != nil {
		return nil, fmt.Errorf("journalstore: %w", err)
	}
	return tools.FileTools(engine, sb, (&fileJournal{store: store, sid: sid, scope: scope}).record), nil
}

// fileJournal records every write_file/edit_file commit in the action
// journal (journalstore) before codetools replaces the target, so each file
// action has a byte-identical inverse (SPEC §4.3 step 8, §18.1, ADR-117).
type fileJournal struct {
	store journal.Store
	sid   string
	scope json.RawMessage
}

func (j *fileJournal) record(ctx context.Context, env tools.Env, tool string, c *codetools.Commit) error {
	kind := journal.FileEdit
	if tool == "write_file" {
		kind = journal.FileWrite
	}
	var before *journal.Snapshot
	afterMode := c.Mode
	if c.Before != nil {
		var err error
		if before, err = snapshot(c.Before, c.Mode); err != nil {
			return fmt.Errorf("journal before image: %w", err)
		}
	} else {
		fi, err := c.After.Stat()
		if err != nil {
			return fmt.Errorf("journal after mode: %w", err)
		}
		afterMode = fi.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	}
	after, err := snapshot(c.After, afterMode)
	if err != nil {
		return fmt.Errorf("journal after image: %w", err)
	}
	_, err = j.store.Record(ctx, &journal.Record{
		SessionID: j.sid, TurnID: env.TurnID, CallID: env.CallID, ToolName: tool,
		Kind: kind, Path: c.Path, Before: before, After: after, Scope: j.scope,
	})
	if err != nil {
		return fmt.Errorf("journal record: %w", err)
	}
	return nil
}

// snapshot reads f whole with ReadAt (its offset is not ours to move).
func snapshot(f *os.File, mode fs.FileMode) (*journal.Snapshot, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	if fi.Size() > maxJournalImage {
		return nil, fmt.Errorf("%d bytes exceeds the 1 GiB undo image cap", fi.Size())
	}
	data := make([]byte, fi.Size())
	n, rerr := f.ReadAt(data, 0)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return nil, fmt.Errorf("read: %w", rerr)
	}
	return &journal.Snapshot{Data: data[:n], Mode: mode, ModeSet: true}, nil
}

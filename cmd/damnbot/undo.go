// `damnbot undo` (SPEC §10, §18.1): restore journaled file actions from the
// shell. A real undo takes a short lease on the action's session and records
// the outcome there; `ls` and `--dry-run` only read. Every file is reached
// only through the sandbox journaled with the action (REVIEW-F2-SEC5-1).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/journal"
	"github.com/damnbot/damnbot/internal/sandbox"
)

type undoOptions struct {
	configPath, dataDir, session string
	dryRun, force, list          bool
	count                        int   // undo N: newest N actions (default 1)
	action                       int64 // undo a<id>: one action; 0 = newest count
}

func runUndo(args []string, stdout, stderr io.Writer) int {
	return runSub("undo", args, stdout, stderr, parseUndoFlags, undoMain)
}

// parseUndoFlags accepts flags before and after the positional argument
// (`undo 3 --dry-run`): `ls`, N, or an action id `a<id>` as printed by ls.
func parseUndoFlags(args []string, stderr io.Writer) (undoOptions, error) {
	o := undoOptions{count: 1}
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be restored, with diffs; change nothing")
	fs.BoolVar(&o.force, "force", false, "overwrite files changed since the action")
	fs.StringVar(&o.session, "session", "", "session id (default: newest session with actions in this directory)")
	fs.StringVar(&o.configPath, "config", "", "config.toml path (default: profile path)")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory (default: SPEC §11)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return undoOptions{}, err
	}
	switch {
	case len(pos) > 1:
		return undoOptions{}, fmt.Errorf("one target expected, got %q", pos)
	case len(pos) == 0:
	case pos[0] == "ls":
		o.list = true
	case strings.HasPrefix(pos[0], "a"):
		id, perr := strconv.ParseInt(pos[0][1:], 10, 64)
		if perr != nil || id <= 0 {
			return undoOptions{}, fmt.Errorf("invalid action id %q (want a<id> from `damnbot undo ls`)", pos[0])
		}
		o.action = id
	default:
		n, perr := strconv.Atoi(pos[0])
		if perr != nil || n <= 0 {
			return undoOptions{}, fmt.Errorf("invalid count %q (want N ≥ 1, a<id>, or ls)", pos[0])
		}
		o.count = n
	}
	return o, nil
}

func undoMain(ctx context.Context, o *undoOptions, stdout io.Writer) (code int, err error) {
	env, err := openCLI(ctx, o.configPath, o.dataDir)
	if err != nil {
		return 1, err
	}
	defer func() {
		if cerr := env.db.CloseContext(ctx); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close ctxdb: %w", cerr))
		}
	}()
	store, err := journalstore.New(env.db, nil)
	if err != nil {
		return 1, fmt.Errorf("journalstore: %w", err)
	}
	if !o.list && !o.dryRun {
		return undoApply(ctx, env, store, o, stdout)
	}
	acts, sid, err := undoTargets(ctx, env.db, store, o)
	if err != nil {
		return 1, err
	}
	if len(acts) == 0 {
		return 1, fmt.Errorf("session %s has no journaled actions", sid)
	}
	return 0, previewActions(ctx, env, store, acts, o.list, stdout)
}

// undoTargets resolves the session — --session, else the newest session of
// this directory with actions — and the actions an undo would touch. An
// explicit a<id> must belong to that session: a global id never authorizes
// an undo in another session or directory (REVIEW-F2-SEC5-2).
func undoTargets(ctx context.Context, db *ctxdb.Store, store *journalstore.Store, o *undoOptions) ([]journal.Action, string, error) {
	sid, err := undoSession(ctx, db, o.session)
	if err != nil {
		return nil, "", err
	}
	if o.action != 0 {
		a, gerr := store.Get(ctx, o.action)
		if gerr != nil {
			return nil, "", fmt.Errorf("action a%d: %w", o.action, gerr)
		}
		if a.SessionID != sid {
			return nil, "", fmt.Errorf("action a%d is not in session %s; refusing (pass --session with its own session)", o.action, sid)
		}
		return []journal.Action{a}, sid, nil
	}
	limit := o.count
	if o.list {
		limit = 0
	}
	acts, err := store.List(ctx, sid, limit)
	if err != nil {
		return nil, sid, fmt.Errorf("list actions: %w", err)
	}
	return acts, sid, nil
}

func undoSession(ctx context.Context, db *ctxdb.Store, session string) (string, error) {
	if session != "" {
		return session, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	const q = `SELECT a.session_id FROM actions a JOIN sessions s ON s.id = a.session_id
		WHERE s.cwd = ? ORDER BY a.id DESC LIMIT 1`
	var sid string
	err = db.Write(ctx, func(tx *ctxdb.Tx) error { return tx.Get(q, []any{cwd}, &sid) })
	if errors.Is(err, ctxdb.ErrNotFound) {
		return "", fmt.Errorf("no journaled actions for %s; pass --session", cwd)
	}
	if err != nil {
		return "", fmt.Errorf("find session: %w", err)
	}
	return sid, nil
}

// undoSandbox rebuilds the sandbox journaled with the actions (roots and
// deny_paths at record time, append-only), never the mutable session row or
// the current roots; the current deny_paths are added, so a path denied now
// stays denied. Actions without a journaled sandbox, or spanning several,
// are refused (fail closed).
func undoSandbox(env *cliEnv, acts []journal.Action) (*sandbox.Sandbox, error) {
	for i := range acts {
		switch {
		case len(acts[i].Scope) == 0:
			return nil, fmt.Errorf("action a%d has no journaled sandbox; refusing to undo it", acts[i].ID)
		case !bytes.Equal(acts[i].Scope, acts[0].Scope):
			return nil, errors.New("the actions were recorded under different sandboxes; undo them one at a time with a<id>")
		}
	}
	var sc undoScope
	if err := json.Unmarshal(acts[0].Scope, &sc); err != nil || len(sc.Roots) == 0 {
		return nil, fmt.Errorf("action a%d has an invalid journaled sandbox; refusing to undo it", acts[0].ID)
	}
	deny := slices.Concat(sc.DenyPaths, expandDeny(env.cfg.DenyPaths(env.configDir), homeDir()))
	sb, err := sandbox.New(&sandbox.Config{Roots: sc.Roots, DenyPaths: deny})
	if err != nil {
		return nil, fmt.Errorf("journaled sandbox: %w", err)
	}
	return sb, nil
}

// undoApply acquires a short lease of the session first (SPEC §18.1: a
// live lease of another process is session_busy), then lists and
// scope-checks the exact action set it executes, so an action appended
// after the check cannot join the batch (SEC5-14). It reports every
// result, including files restored before a recording failure.
func undoApply(ctx context.Context, env *cliEnv, store *journalstore.Store, o *undoOptions, w io.Writer) (code int, err error) {
	sid, err := undoSession(ctx, env.db, o.session)
	if err != nil {
		return 1, err
	}
	if _, lerr := env.db.AcquireLease(ctx, sid); lerr != nil {
		if errors.Is(lerr, ctxdb.ErrSessionBusy) {
			return 4, fmt.Errorf("%w; run the undo from the process that holds the session", lerr)
		}
		return undoExit(lerr), fmt.Errorf("lease: %w", lerr)
	}
	defer func() {
		if rerr := env.db.ReleaseLease(ctx, sid); rerr != nil {
			err = errors.Join(err, fmt.Errorf("release lease: %w", rerr))
		}
	}()
	acts, _, err := undoTargets(ctx, env.db, store, o)
	if err != nil {
		return 1, err
	}
	if len(acts) == 0 {
		return 1, fmt.Errorf("session %s has no journaled actions", sid)
	}
	sb, err := undoSandbox(env, acts)
	if err != nil {
		return 1, err
	}
	undoer, err := journal.NewUndoer(store, sb)
	if err != nil {
		return 1, fmt.Errorf("undoer: %w", err)
	}
	defer func() {
		if cerr := undoer.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close undoer: %w", cerr))
		}
	}()
	var results []journal.Result
	var uerr error
	if o.action != 0 {
		var r journal.Result
		r, uerr = undoer.Undo(ctx, sid, o.action, o.force)
		results = []journal.Result{r}
	} else {
		results, uerr = undoer.UndoActions(ctx, sid, acts, o.force)
	}
	for i := range results {
		r := &results[i]
		if _, werr := fmt.Fprintf(w, "a%d %s %s\n", r.ActionID, r.Status, r.Detail); werr != nil {
			return 1, fmt.Errorf("write output: %w", werr)
		}
		if r.Status != journal.Undone {
			code = 1
		}
	}
	if uerr != nil {
		return max(code, undoExit(uerr)), uerr
	}
	return code, nil
}

// undoExit maps store refusals to the CLI exit codes of SPEC §4.3.
func undoExit(err error) int {
	if errors.Is(err, ctxdb.ErrSessionBusy) || errors.Is(err, ctxdb.ErrLeaseFenced) || errors.Is(err, ctxdb.ErrDBBusy) {
		return 4
	}
	return 1
}

// previewActions prints `undo ls` rows or the `--dry-run` verdict and diff
// of each action. Every untrusted field is sanitized for the terminal
// (REVIEW-F2-SEC5-4). The verdicts mirror journal.Undoer: current = before
// is a no-op, current = after restores, anything else is a conflict.
func previewActions(ctx context.Context, env *cliEnv, store *journalstore.Store, acts []journal.Action, list bool, w io.Writer) error {
	var b strings.Builder
	for i := range acts {
		a := &acts[i]
		fmt.Fprintf(&b, "a%d %s %s %s", a.ID, time.UnixMilli(a.CreatedAt).Format(time.RFC3339), a.Kind, cleanLine(a.Path))
		if a.MoveTo != "" {
			fmt.Fprintf(&b, " -> %s", cleanLine(a.MoveTo))
		}
		if list {
			b.WriteString(undoFlags(a) + "\n")
			continue
		}
		verdict, diff, err := dryRun(ctx, env, store, a)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, ": %s\n%s", verdict, diff)
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func undoFlags(a *journal.Action) string {
	switch {
	case a.Expired:
		return " [expired]"
	case a.Open:
		return " [turn open]"
	}
	return ""
}

func dryRun(ctx context.Context, env *cliEnv, store *journalstore.Store, a *journal.Action) (verdict, diff string, err error) {
	switch {
	case a.Expired:
		return "would refuse: expired (outside the retention window)", "", nil
	case a.Open:
		return "would refuse: the action's turn is still open", "", nil
	case a.Kind == journal.FileMove:
		return "would move " + cleanLine(a.MoveTo) + " back (verified at undo time)", "", nil
	}
	sb, err := undoSandbox(env, []journal.Action{*a})
	if err != nil {
		return "refused: " + cleanLine(err.Error()), "", nil // a refusal is the dry-run verdict
	}
	cur, refusal := readInSandbox(ctx, sb, a.Path)
	if refusal != "" {
		return "refused: " + refusal, "", nil
	}
	before, err := image(ctx, store, a.Before)
	if err != nil {
		return "", "", err
	}
	switch {
	case matches(cur, a.Before):
		return "no-op (already at the before image)", "", nil
	case matches(cur, a.After) && a.Before == nil:
		return "would remove the file the action created", "", nil
	case matches(cur, a.After):
		return "would restore the before image", lineDiff(cur, before), nil
	}
	after, err := image(ctx, store, a.After)
	if err != nil {
		return "", "", err
	}
	return "conflict: file changed since the action (--force overwrites)",
		"changed since the action:\n" + lineDiff(after, cur) + "--force would apply:\n" + lineDiff(cur, before), nil
}

// readInSandbox reads p the way the undo executor reaches it (SEC5-3,
// SEC5-15): the action's sandbox must classify it inside a root, it must
// still resolve to itself, and it is opened with OpenResolved — no symlink
// on any component, deny_paths re-checked on the handle's path — so an
// ancestor swapped for a symlink, out of the root or into a denied
// directory inside it, is neither read nor shown. Only a regular file
// within the image cap is read. nil data = absent; a refusal never reads.
func readInSandbox(ctx context.Context, sb *sandbox.Sandbox, p string) (data []byte, refusal string) {
	v, resolved, err := sb.Check(ctx, p, sandbox.OpWrite)
	if err != nil || (v != sandbox.Inside && v != sandbox.HardLinked) || filepath.Clean(p) != resolved {
		return nil, "outside the action's sandbox or denied"
	}
	f, _, err := sb.OpenResolved(ctx, resolved, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	if err != nil {
		return nil, "outside the action's sandbox or denied"
	}
	defer f.Close() //nolint:errcheck // read-only handle; the read error is authoritative.
	fi, serr := f.Stat()
	if serr != nil || !fi.Mode().IsRegular() {
		return nil, "not a regular file (symlink or special file)"
	}
	if fi.Size() > maxJournalImage {
		return nil, "larger than the undo image cap"
	}
	data, rerr := io.ReadAll(io.LimitReader(f, maxJournalImage+1))
	if rerr != nil || len(data) > maxJournalImage {
		return nil, "changed or unreadable while checking"
	}
	if data == nil {
		data = []byte{}
	}
	return data, ""
}

// matches reports whether cur (nil = absent) is the recorded image.
func matches(cur []byte, img *journal.Image) bool {
	if img == nil {
		return cur == nil
	}
	sum := sha256.Sum256(cur)
	return cur != nil && hex.EncodeToString(sum[:]) == img.BlobSHA
}

func image(ctx context.Context, store *journalstore.Store, img *journal.Image) ([]byte, error) {
	if img == nil {
		return nil, nil
	}
	data, err := store.Blob(ctx, img.BlobSHA)
	if err != nil {
		return nil, fmt.Errorf("journal image: %w", err)
	}
	return data, nil
}

// lineDiff is the edit_file diff of a → b, sanitized line by line.
func lineDiff(a, b []byte) string {
	if bytes.IndexByte(a, 0) >= 0 || bytes.IndexByte(b, 0) >= 0 {
		return fmt.Sprintf("(binary: %d → %d bytes)\n", len(a), len(b))
	}
	d, ok := codetools.UnifiedDiff(a, b)
	if !ok {
		return fmt.Sprintf("(diff omitted: too many changes, %d → %d bytes)\n", len(a), len(b))
	}
	return cleanBlock(d)
}

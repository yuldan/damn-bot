// `damnbot review` (SPEC §10, §4.8, P10): the memory quarantine queue. Only
// the user approves; entries are listed and decided per scope.
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
	"unicode"

	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
)

type reviewOptions struct {
	configPath, dataDir string
	scopes              []string
	rejected, bulk      bool
	action              string  // "" = list, "approve", "reject"
	ids                 []int64 // explicit targets of approve/reject
}

func runReview(args []string, stdout, stderr io.Writer) int {
	return runSub("review", args, stdout, stderr, parseReviewFlags, reviewMain)
}

// parseReviewFlags: `review [--rejected]` lists; `review approve|reject
// <id>...` or `--bulk` (every pending entry of the scopes) decides.
func parseReviewFlags(args []string, stderr io.Writer) (reviewOptions, error) {
	var o reviewOptions
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.rejected, "rejected", false, "list rejected entries instead of pending ones")
	fs.BoolVar(&o.bulk, "bulk", false, "approve/reject every pending entry of the scopes")
	fs.Func("scope", "memory scope to review (repeatable; default: user and project:<cwd>)", func(s string) error {
		o.scopes = append(o.scopes, s)
		return nil
	})
	fs.StringVar(&o.configPath, "config", "", "config.toml path (default: profile path)")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory (default: SPEC §11)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return reviewOptions{}, err
	}
	if len(pos) > 0 {
		o.action, pos = pos[0], pos[1:]
	}
	if err := o.validate(len(pos) > 0); err != nil {
		return reviewOptions{}, err
	}
	for _, p := range pos {
		id, perr := strconv.ParseInt(p, 10, 64)
		if perr != nil || id <= 0 {
			return reviewOptions{}, fmt.Errorf("invalid id %q", p)
		}
		o.ids = append(o.ids, id)
	}
	return o, nil
}

// validate checks the action shape: list, or approve/reject with ids xor
// --bulk.
func (o *reviewOptions) validate(hasIDs bool) error {
	switch {
	case o.action != "" && o.action != "approve" && o.action != "reject":
		return fmt.Errorf("unknown action %q (want approve or reject)", o.action)
	case o.action == "" && (hasIDs || o.bulk):
		return errors.New("ids and --bulk need approve or reject")
	case o.action != "" && hasIDs == o.bulk:
		return errors.New("approve/reject need ids or --bulk (not both)")
	case o.action != "" && o.rejected:
		return errors.New("--rejected only lists")
	}
	return nil
}

func reviewMain(ctx context.Context, o *reviewOptions, w io.Writer) (code int, err error) {
	env, err := openCLI(ctx, o.configPath, o.dataDir)
	if err != nil {
		return 1, err
	}
	defer func() {
		if cerr := env.db.CloseContext(ctx); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close ctxdb: %w", cerr))
		}
	}()
	svc, err := openMemory(env.db, redact.New(redact.Options{}))
	if err != nil {
		return 1, err
	}
	scopes := o.scopes
	if len(scopes) == 0 {
		cwd, gerr := os.Getwd()
		if gerr != nil {
			return 1, fmt.Errorf("getwd: %w", gerr)
		}
		scopes = []string{"user", "project:" + cwd}
	}
	list := svc.Pending
	if o.rejected {
		list = svc.Rejected
	}
	hits, err := list(ctx, scopes, memory.MaxRecall)
	if err != nil {
		return 1, fmt.Errorf("list: %w", err)
	}
	if o.action == "" {
		return 0, writeReview(w, hits)
	}
	return decideReview(ctx, svc, o, hits, w)
}

// decideReview approves or rejects the targets; an explicit id must be a
// pending entry of the reviewed scopes (no cross-scope decisions, JN3-1).
func decideReview(ctx context.Context, svc *memory.Service, o *reviewOptions, pending []memory.Hit, w io.Writer) (int, error) {
	targets := make([]memory.Record, 0, len(pending))
	for i := range pending {
		if o.bulk || slices.Contains(o.ids, pending[i].Record.ID) {
			targets = append(targets, pending[i].Record)
		}
	}
	var errs []error
	for _, id := range o.ids {
		if !slices.ContainsFunc(targets, func(r memory.Record) bool { return r.ID == id }) {
			errs = append(errs, fmt.Errorf("%d is not pending in the reviewed scopes (see --scope)", id))
		}
	}
	decide, verb := svc.Approve, "approved"
	if o.action == "reject" {
		decide, verb = svc.Reject, "rejected"
	}
	var b strings.Builder
	for i := range targets {
		if derr := decide(ctx, targets[i].ID, targets[i].Scope); derr != nil {
			errs = append(errs, fmt.Errorf("%d: %w", targets[i].ID, derr))
			continue
		}
		fmt.Fprintf(&b, "%d %s\n", targets[i].ID, verb)
	}
	if _, werr := io.WriteString(w, b.String()); werr != nil {
		errs = append(errs, fmt.Errorf("write output: %w", werr))
	}
	if err := errors.Join(errs...); err != nil {
		return 1, err
	}
	return 0, nil
}

// oneLine flattens whitespace and drops control characters, so quarantined
// text cannot drive the reviewer's terminal (escape sequences).
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// writeReview prints one line per entry: id, status, scope, category,
// origin, scan severity and the (already redacted) content on one line.
func writeReview(w io.Writer, hits []memory.Hit) error {
	var b strings.Builder
	for i := range hits {
		r := &hits[i].Record
		var scan memory.Scan
		sev := "-"
		if json.Unmarshal([]byte(r.ScanResult), &scan) == nil && scan.Severity != "" {
			sev = string(scan.Severity)
		}
		fmt.Fprintf(&b, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Status, r.Scope, r.Category, r.Origin, sev, oneLine(r.Content))
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

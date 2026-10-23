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
	"strconv"
	"strings"

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
	if o.action != "" {
		return decideReview(ctx, svc, o, scopes, w)
	}
	list := svc.Pending
	if o.rejected {
		list = svc.Rejected
	}
	hits, err := list(ctx, scopes, memory.MaxRecall)
	if err != nil {
		return 1, fmt.Errorf("list: %w", err)
	}
	return 0, writeReview(w, hits, len(hits) == memory.MaxRecall)
}

// decideReview approves or rejects every pending entry of the scopes
// (--bulk: page after page until the queue is empty, REVIEW-F2-SEC5-7) or
// exactly the given ids, each of which must be pending in one of the
// reviewed scopes (no cross-scope decision, JN3-1).
func decideReview(ctx context.Context, svc *memory.Service, o *reviewOptions, scopes []string, w io.Writer) (int, error) {
	decide, verb := svc.Approve, "approved"
	if o.action == "reject" {
		decide, verb = svc.Reject, "rejected"
	}
	var b strings.Builder
	var errs []error
	failed := map[int64]bool{}
	apply := func(recs []memory.Record) (tried int) {
		for i := range recs {
			if failed[recs[i].ID] {
				continue
			}
			tried++
			if derr := decide(ctx, recs[i].ID, recs[i].Scope); derr != nil {
				failed[recs[i].ID] = true
				errs = append(errs, fmt.Errorf("%d: %w", recs[i].ID, derr))
				continue
			}
			fmt.Fprintf(&b, "%d %s\n", recs[i].ID, verb)
		}
		return tried
	}
	if o.bulk {
		if err := drainPending(ctx, svc, scopes, apply); err != nil {
			errs = append(errs, err)
		}
	} else {
		recs, terr := pendingByID(ctx, svc, o.ids, scopes)
		errs = append(errs, terr...)
		apply(recs)
	}
	if _, werr := io.WriteString(w, b.String()); werr != nil {
		errs = append(errs, fmt.Errorf("write output: %w", werr))
	}
	if err := errors.Join(errs...); err != nil {
		return 1, err
	}
	return 0, nil
}

// drainPending feeds every pending entry of scopes to apply, page after
// page, until the queue is empty or a page holds only entries apply skips
// (already failed), so a failing entry cannot loop forever.
func drainPending(ctx context.Context, svc *memory.Service, scopes []string, apply func([]memory.Record) int) error {
	for {
		hits, err := svc.Pending(ctx, scopes, memory.MaxRecall)
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}
		recs := make([]memory.Record, len(hits))
		for i := range hits {
			recs[i] = hits[i].Record
		}
		if apply(recs) == 0 {
			return nil
		}
	}
}

// pendingByID resolves explicit ids without the listing cap: each must be
// scanned and in one of the reviewed scopes (compared in normalized form).
func pendingByID(ctx context.Context, svc *memory.Service, ids []int64, scopes []string) ([]memory.Record, []error) {
	norm := make(map[string]bool, len(scopes))
	for _, sc := range scopes {
		n, err := memory.NormalizeScope(sc)
		if err != nil {
			return nil, []error{fmt.Errorf("scope %q: %w", sc, err)}
		}
		norm[n] = true
	}
	recs, err := svc.Get(ctx, ids)
	if err != nil {
		return nil, []error{fmt.Errorf("get: %w", err)}
	}
	byID := make(map[int64]memory.Record, len(recs))
	for i := range recs {
		byID[recs[i].ID] = recs[i]
	}
	var out []memory.Record
	var errs []error
	for _, id := range ids {
		r, ok := byID[id]
		if !ok || r.Status != memory.StatusScanned || !norm[r.Scope] {
			errs = append(errs, fmt.Errorf("%d is not pending in the reviewed scopes (see --scope)", id))
			continue
		}
		out = append(out, r)
	}
	return out, errs
}

// writeReview prints one line per entry: id, status, scope, category,
// origin, scan severity and the (already redacted) content. Every field is
// terminal-sanitized (REVIEW-F2-SEC5-6); a full page ends with a note that
// more entries may be queued.
func writeReview(w io.Writer, hits []memory.Hit, more bool) error {
	var b strings.Builder
	for i := range hits {
		r := &hits[i].Record
		var scan memory.Scan
		sev := "-"
		if json.Unmarshal([]byte(r.ScanResult), &scan) == nil && scan.Severity != "" {
			sev = string(scan.Severity)
		}
		fmt.Fprintf(&b, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, cleanLine(string(r.Status)), cleanLine(r.Scope),
			cleanLine(string(r.Category)), cleanLine(string(r.Origin)), cleanLine(sev), cleanLine(r.Content))
	}
	if more {
		fmt.Fprintf(&b, "(first %d shown; decide them or narrow --scope to see the rest; --bulk covers all)\n", len(hits))
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

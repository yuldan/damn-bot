package heal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Log retention of SPEC §17: `<state_dir>/logs/damnbot.log`, 10 MB × 3.
const (
	DefaultMaxLogBytes = 10 << 20
	DefaultKeepLogs    = 3
)

// RecoverTurns closes open turn journals at start (SELF-HEALING §3.4,
// SPEC §11.11.8) through the ctxdb API, so each row gets its spec status,
// `turn_end{stop_reason:"crashed"}` and `crash_recovered`. background is true
// only for serve's 60 s scanner: case (b) needs two observations ≥ 3 s apart
// and must not block start (P12).
func RecoverTurns(ctx context.Context, s *ctxdb.Store, background bool) ([]ctxdb.Recovered, error) {
	if s == nil {
		return nil, nil
	}
	out, err := s.RecoverJournals(ctx, background)
	if err != nil {
		return nil, fmt.Errorf("heal: recover turns: %w", err)
	}
	return out, nil
}

// RepairOptions configures the repairs; nil Store and empty LogDir skip steps.
type RepairOptions struct {
	Store       *ctxdb.Store // orphan turns + stale session leases, integrity
	LogDir      string       // <state_dir>/logs
	MaxLogBytes int64        // default DefaultMaxLogBytes
	KeepLogs    int          // default DefaultKeepLogs
}

// Repair runs the safe repairs of SELF-HEALING §4/§8: stale session leases and
// orphan turns are reclaimed by ctxdb's takeover_ok rules, integrity is checked
// read-only, oversized logs rotated. It never deletes user data (only rotated
// logs beyond retention); every step is idempotent.
type Repair struct{ o RepairOptions }

// NewRepair fills the defaults and returns a reusable Repair.
func NewRepair(o RepairOptions) *Repair {
	if o.MaxLogBytes <= 0 {
		o.MaxLogBytes = DefaultMaxLogBytes
	}
	if o.KeepLogs <= 0 {
		o.KeepLogs = DefaultKeepLogs
	}
	return &Repair{o: o}
}

// StepResult is one repair step and what it found.
type StepResult struct {
	Name    string `json:"name"`
	Changed bool   `json:"changed"`
	Detail  string `json:"detail"`
}

// Report is the ordered outcome of a repair run.
type Report struct {
	Steps   []StepResult `json:"steps"`
	Changed bool         `json:"changed"`
}

// Run executes every independent step once and joins their errors.
func (r *Repair) Run(ctx context.Context) (Report, error) {
	var rep Report
	var errs []error
	for _, st := range []struct {
		name string
		fn   func(context.Context) (bool, string, error)
	}{{"journals", r.journals}, {"integrity", r.integrity}, {"logs", r.logs}} {
		changed, detail, err := st.fn(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("heal: repair %s: %w", st.name, err))
			detail = "failed: " + err.Error()
		}
		rep.Steps = append(rep.Steps, StepResult{Name: st.name, Changed: changed, Detail: detail})
		rep.Changed = rep.Changed || changed
	}
	return rep, errors.Join(errs...)
}

// journals reclaims orphan turns and the stale leases they hold.
func (r *Repair) journals(ctx context.Context) (bool, string, error) {
	if r.o.Store == nil {
		return false, "skipped: no store", nil
	}
	out, err := RecoverTurns(ctx, r.o.Store, false)
	if err != nil {
		return false, "", err
	}
	if len(out) == 0 {
		return false, "no open turns", nil
	}
	ask := 0
	for i := range out {
		if out[i].Decision == "ask" {
			ask++
		}
	}
	slog.WarnContext(ctx, "heal: recovered turns", "turns", len(out), "awaiting_confirmation", ask)
	return true, fmt.Sprintf("%d recovered, %d awaiting confirmation", len(out), ask), nil
}

// integrity runs PRAGMA integrity_check read-only (SELF-HEALING §3.5).
func (r *Repair) integrity(ctx context.Context) (bool, string, error) {
	if r.o.Store == nil {
		return false, "skipped: no store", nil
	}
	res, err := r.o.Store.IntegrityCheck(ctx)
	if err != nil {
		return false, res, fmt.Errorf("integrity_check: %w", err)
	}
	return false, res, nil
}

// logs rotates files above MaxLogBytes, keeping KeepLogs generations.
func (r *Repair) logs(ctx context.Context) (bool, string, error) {
	if r.o.LogDir == "" {
		return false, "skipped: no log dir", nil
	}
	n, err := rotateLogs(r.o.LogDir, r.o.MaxLogBytes, r.o.KeepLogs)
	if err != nil {
		return false, "", err
	}
	if n == 0 {
		return false, "no log above limit", nil
	}
	slog.WarnContext(ctx, "heal: rotated logs", "files", n)
	return true, fmt.Sprintf("%d log(s) rotated", n), nil
}

// rotateLogs shifts `<name>.log` to `.log.1` and older copies up, dropping the
// one beyond keep; a missing directory is a no-op.
func rotateLogs(dir string, maxBytes int64, keep int) (n int, err error) {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("heal: open log dir: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	d, err := root.Open(".")
	if err != nil {
		return 0, fmt.Errorf("heal: list log dir: %w", err)
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	entries, err := d.ReadDir(-1)
	if err != nil {
		return 0, fmt.Errorf("heal: list log dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		fi, serr := e.Info()
		if serr != nil {
			return n, fmt.Errorf("heal: stat log %s: %w", e.Name(), serr)
		}
		if fi.Size() <= maxBytes {
			continue
		}
		if rerr := shiftLog(root, e.Name(), keep); rerr != nil {
			return n, rerr
		}
		n++
	}
	return n, nil
}

// shiftLog drops `<name>.K` and shifts each older copy up one generation.
func shiftLog(root *os.Root, name string, keep int) error {
	if err := root.Remove(name + "." + strconv.Itoa(keep)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("heal: drop oldest %s: %w", name, err)
	}
	for i := keep - 1; i >= 1; i-- {
		from, to := name+"."+strconv.Itoa(i), name+"."+strconv.Itoa(i+1)
		if err := root.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("heal: rotate %s: %w", from, err)
		}
	}
	if err := root.Rename(name, name+".1"); err != nil {
		return fmt.Errorf("heal: rotate %s: %w", name, err)
	}
	return nil
}

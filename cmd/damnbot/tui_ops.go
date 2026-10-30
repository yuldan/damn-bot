// TUI session/action/clipboard seam (A4): real ctxdb/journal work behind the
// internal/tui interfaces, which stay I/O free (E-09).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/tools"
	"github.com/damnbot/damnbot/internal/tui"
)

type tuiActions struct {
	db         func(context.Context) (*ctxdb.Store, error)
	cfg        *config.Config
	configHash func() string
	configDir  string
	modelRef   string
	active     func(string) bool
}

func (a *tuiActions) New(ctx context.Context) (tui.SessionChange, error) {
	db, err := a.db(ctx)
	if err != nil {
		return tui.SessionChange{}, err
	}
	sid, _, err := newRunSession(ctx, db, a.modelRef, a.configHash(), "", "tui")
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("new session: %w", err)
	}
	return tui.SessionChange{SessionID: sid, Detail: "new session " + sid}, nil
}

// Fork branches at the current head (SPEC §4.6); the parent is untouched.
func (a *tuiActions) Fork(ctx context.Context, sessionID string) (tui.SessionChange, error) {
	db, err := a.db(ctx)
	if err != nil {
		return tui.SessionChange{}, err
	}
	head, err := db.Head(ctx, sessionID)
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("fork: %w", err)
	}
	child, err := db.Fork(ctx, sessionID, head)
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("fork: %w", err)
	}
	return tui.SessionChange{SessionID: child, Detail: fmt.Sprintf("forked %s at seq %d -> %s", sessionID, head, child)}, nil
}

// Rewind is a fork at an earlier seq/point (SPEC §4.6); the old branch stays.
func (a *tuiActions) Rewind(ctx context.Context, sessionID, at string) (tui.SessionChange, error) {
	db, err := a.db(ctx)
	if err != nil {
		return tui.SessionChange{}, err
	}
	target, _, err := branchPoint(ctx, db, sessionID, at, true)
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("rewind: %w", err)
	}
	child, err := db.Fork(ctx, sessionID, target)
	if err != nil {
		return tui.SessionChange{}, fmt.Errorf("rewind: %w", err)
	}
	return tui.SessionChange{SessionID: child, Detail: fmt.Sprintf("rewound %s at seq %d -> %s", sessionID, target, child)}, nil
}

// Undo runs the existing journal path (lease, conflict, retention, sandbox).
func (a *tuiActions) Undo(ctx context.Context, sessionID, target string) (string, error) {
	if a.active != nil && a.active(sessionID) {
		return "", errors.New("a turn is running; cancel it first")
	}
	db, err := a.db(ctx)
	if err != nil {
		return "", err
	}
	store, err := journalstore.New(db, nil)
	if err != nil {
		return "", fmt.Errorf("undo: %w", err)
	}
	o := &undoOptions{session: sessionID, count: 1}
	if target != "" {
		id, perr := strconv.ParseInt(strings.TrimPrefix(target, "a"), 10, 64)
		if perr != nil || id <= 0 || !strings.HasPrefix(target, "a") {
			return "", fmt.Errorf("invalid action %q (want a<id>)", target)
		}
		o.action = id
	}
	var out strings.Builder
	code, uerr := undoApply(ctx, &cliEnv{cfg: *a.cfg, configDir: a.configDir, db: db}, store, o, &out)
	detail := strings.TrimSpace(out.String())
	switch {
	case uerr != nil:
		return detail, uerr
	case code != 0:
		return detail, errors.New(detail)
	}
	return detail, nil
}

func (a *tuiActions) List(ctx context.Context) ([]tui.SessionInfo, error) {
	db, err := a.db(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := db.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	if len(ids) > 50 {
		ids = ids[:50]
	}
	out := make([]tui.SessionInfo, 0, len(ids))
	for _, id := range ids {
		cwd, model, xerr := sessionMeta(ctx, db, id)
		if xerr != nil {
			continue
		}
		out = append(out, tui.SessionInfo{ID: id, Cwd: cwd, Model: model})
	}
	return out, nil
}

type clipboardAdapter struct{}

func (clipboardAdapter) Copy(text string) error {
	if err := clipboard.WriteAll(text); err != nil {
		return fmt.Errorf("system clipboard: %w", err)
	}
	return nil
}

func (r *tuiRunner) Modules(ctx context.Context) ([]tui.ModuleInfo, error) {
	model, err := r.ensure(ctx)
	if err != nil {
		return nil, err
	}
	var out []tui.ModuleInfo
	for _, d := range modules.Default().Descriptors() {
		status := "stopped"
		if model.modules.runtime != nil {
			status = string(model.modules.runtime.Status(d.ID))
		}
		out = append(out, tui.ModuleInfo{ID: d.ID, Group: d.Group, Status: status})
	}
	return out, nil
}

// Context reports the model window this run resolved (registry or config).
func (r *tuiRunner) Context(ctx context.Context) (tui.ContextInfo, error) {
	model, err := r.ensure(ctx)
	if err != nil {
		return tui.ContextInfo{}, err
	}
	return tui.ContextInfo{Window: model.window, Source: model.winSource}, nil
}

// configuredModels lists the refs the config actually names, deduplicated.
func configuredModels(cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(ref string) {
		if ref != "" && !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	add(cfg.Provider.Model)
	add(cfg.Provider.Light)
	for _, ref := range cfg.Provider.Fallback {
		add(ref)
	}
	for i := range cfg.Providers.Custom {
		p := &cfg.Providers.Custom[i]
		for _, m := range p.Models {
			add(p.Name + "/" + m.ID)
		}
	}
	return out
}

func snapshotFor(cfg *config.Config, modelRef string) tui.Config {
	return tui.Config{
		Model: modelRef, Theme: cfg.UI.Theme,
		ApprovalTimeout: timeoutOf(cfg), Models: configuredModels(cfg),
		Budget: tui.BudgetInfo{TurnUSD: cfg.Budget.TurnUSD, SessionUSD: cfg.Budget.SessionUSD, DailyUSD: cfg.Budget.DailyUSD},
	}
}

func timeoutOf(cfg *config.Config) time.Duration {
	return time.Duration(cfg.Approval.PreviewTimeoutS) * time.Second
}

func hashConfig(cfg *config.Config) string {
	data, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r *tuiRunner) ConfigHash() string {
	r.snapMu.RLock()
	defer r.snapMu.RUnlock()
	return r.cfgHash
}

func (r *tuiRunner) Reload(ctx context.Context) (tui.ReloadResult, error) {
	path := r.deps.cfgFile
	if path == "" {
		return tui.ReloadResult{}, errors.New("no config file path is known")
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return tui.ReloadResult{}, fmt.Errorf("config deps: %w", err)
	}
	fresh, err := loader.Load(path)
	if err != nil {
		return tui.ReloadResult{}, fmt.Errorf("%s: %w", path, err)
	}
	if !reflect.DeepEqual(fresh.Provider, r.deps.cfg.Provider) || !reflect.DeepEqual(fresh.Providers, r.deps.cfg.Providers) {
		return tui.ReloadResult{}, errors.New("provider/base_url identity changed; restart damnbot")
	}
	res := tui.ReloadResult{Hash: hashConfig(&fresh)}
	r.snapMu.Lock()
	old := r.snapshot
	r.snapshot = snapshotFor(&fresh, r.deps.modelRef)
	r.cfgHash = res.Hash
	r.snapMu.Unlock()
	if old.Theme != fresh.UI.Theme {
		res.Applied = append(res.Applied, "theme "+fresh.UI.Theme)
	}
	if old.Budget.TurnUSD != fresh.Budget.TurnUSD || old.Budget.SessionUSD != fresh.Budget.SessionUSD || old.Budget.DailyUSD != fresh.Budget.DailyUSD {
		res.Applied = append(res.Applied, "budget caps")
	}
	if old.ApprovalTimeout != timeoutOf(&fresh) {
		res.Applied = append(res.Applied, "approval timeout")
	}
	if runtimeConfigChanged(r.deps.cfg, &fresh) {
		res.Restart = append(res.Restart, "loop/context/tools/skills/storage")
	}
	return res, nil
}

func runtimeConfigChanged(old, fresh *config.Config) bool {
	a, b := *old, *fresh
	a.UI, b.UI = config.UI{}, config.UI{}
	a.Budget, b.Budget = config.Budget{}, config.Budget{}
	a.Approval.PreviewTimeoutS, b.Approval.PreviewTimeoutS = 0, 0
	return !reflect.DeepEqual(a, b)
}

func (r *tuiRunner) Diff(ctx context.Context, sessionID string) (string, error) {
	model, err := r.ensure(ctx)
	if err != nil {
		return "", err
	}
	store, err := journalstore.New(model.db, nil)
	if err != nil {
		return "", fmt.Errorf("diff: %w", err)
	}
	acts, err := store.List(ctx, sessionID, 20)
	if err != nil {
		return "", fmt.Errorf("diff: %w", err)
	}
	env := &cliEnv{cfg: *r.deps.cfg, configDir: r.deps.configDir, db: model.db}
	var b strings.Builder
	for i := range acts {
		a := &acts[i]
		verdict, d, derr := dryRun(ctx, env, store, a)
		fmt.Fprintf(&b, "a%d %s %s: %s\n", a.ID, a.Kind, cleanLine(a.Path), verdict)
		if derr != nil {
			fmt.Fprintf(&b, "  %s\n", cleanLine(derr.Error()))
			continue
		}
		if d != "" {
			b.WriteString(d)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func (r *tuiRunner) Compact(ctx context.Context, sessionID string) (string, error) {
	if r.Active(sessionID) {
		return "", errors.New("a turn is running; cancel it first")
	}
	model, err := r.ensure(ctx)
	if err != nil {
		return "", err
	}
	asm, _, err := newRunAssembler(r.deps.cfg, r.deps.red, nil)
	if err != nil {
		return "", err
	}
	opts := *r.deps.opts
	ctxTier, err := asm.Context(ctx, contextTierInput(ctx, model, &opts, false))
	if err != nil {
		return "", fmt.Errorf("compact context: %w", err)
	}
	toolList, err := runTools(model.registry, model.toolsets.Allowed)
	if err != nil {
		return "", err
	}
	comp, window, err := newRunCompactor(ctx, model, r.deps.modelRef, asm, toolList, ctxTier.Text, r.deps.red)
	if err != nil {
		return "", err
	}
	res, err := comp.Run(ctx, sessionID, compaction.ReasonEstimate)
	if err != nil {
		return "", fmt.Errorf("compaction: %w", err)
	}
	return fmt.Sprintf("compaction %s (estimate %d/%d)", res.Outcome, res.Estimate, window), nil
}

func (r *tuiRunner) Review(ctx context.Context) (string, error) {
	var b strings.Builder
	code, err := reviewMain(ctx, &reviewOptions{configPath: r.deps.cfgFile, dataDir: r.deps.dataDir}, &b)
	if err != nil {
		return "", fmt.Errorf("review: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("review exited %d", code)
	}
	return strings.TrimSpace(b.String()), nil
}

type filesAdapter struct{}

func (filesAdapter) Write(ctx context.Context, path string, data []byte) (abs string, err error) {
	if cerr := ctx.Err(); cerr != nil {
		return "", fmt.Errorf("write cancelled: %w", cerr)
	}
	abs, err = writePath(path)
	if err != nil {
		return "", err
	}
	tmp, terr := os.CreateTemp(filepath.Dir(abs), ".damnbot-write-*")
	if terr != nil {
		return abs, fmt.Errorf("temp file for %s: %w", abs, terr)
	}
	name := tmp.Name()
	defer func() {
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("cleanup %s: %w", name, rerr))
		}
	}()
	if cerr := os.Chmod(name, 0o600); cerr != nil {
		return abs, errors.Join(fmt.Errorf("chmod %s: %w", name, cerr), tmp.Close())
	}
	if _, werr := tmp.Write(data); werr != nil {
		return abs, errors.Join(fmt.Errorf("write %s: %w", name, werr), tmp.Close())
	}
	if cerr := tmp.Close(); cerr != nil {
		return abs, fmt.Errorf("close %s: %w", name, cerr)
	}
	if rerr := os.Rename(name, abs); rerr != nil {
		return abs, fmt.Errorf("rename %s: %w", abs, rerr)
	}
	return abs, nil
}

func writePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("write path is empty")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("write path contains control characters")
		}
	}
	if slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..") {
		return "", errors.New("write path must not contain a parent directory component")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return abs, nil
}

// Bang executes !cmd through the same dispatcher/approval pipeline as shell.
func (r *tuiRunner) Bang(ctx context.Context, sessionID, command string, planMode bool) (tui.BangResult, error) {
	if r.Active(sessionID) {
		return tui.BangResult{}, errors.New("a turn is running; cancel it first")
	}
	model, err := r.ensure(ctx)
	if err != nil {
		return tui.BangResult{}, err
	}
	disp, err := newRunDispatcher(r.deps.opts, model, r.deps.red, model.spill)
	if err != nil {
		return tui.BangResult{}, err
	}
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		return tui.BangResult{}, fmt.Errorf("bang args: %w", err)
	}
	start := time.Now()
	id := fmt.Sprintf("bang-%d", start.UnixNano())
	out := disp.Dispatch(ctx, tools.Call{TurnID: id, CallID: id, Name: "shell", Args: args, PlanMode: planMode})
	return tui.BangResult{
		Output: out.Render(), OK: out.Err == nil && !out.Denied && !out.Cancelled,
		Denied: out.Denied, Duration: time.Since(start),
	}, nil
}

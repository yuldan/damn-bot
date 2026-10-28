// TUI composition seam (T1): loop stream, clock, and lazy session opening.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tui"
)

// runTUI is the bare `damnbot` surface: config and the session id are the only
// work before the first frame.
func runTUI(args []string) int {
	plain := slices.Contains(args, "--plain")

	o := &runOptions{sessionOrigin: "tui", approvalOrigin: loop.OriginTUI, approval: "ask"}
	bridge := &approvalBridge{}
	o.approver, o.toolApprover = loopApprover{bridge}, toolsApprover{bridge}
	cfg, dataDir, configDir, home, err := loadRunEnv(o)
	if err != nil {
		return tuiFail(err)
	}
	benchTUIOverrides(o, &cfg)
	benchTrace("config-done")
	sid, err := newSessionID()
	if err != nil {
		return tuiFail(err)
	}
	configPath, err := tuiConfigPath(o)
	if err != nil {
		return tuiFail(err)
	}
	o.newSessionID = sid
	r := newTUIRunner(&tuiDeps{
		cfg: &cfg, dataDir: dataDir, configDir: configDir, cfgFile: configPath, home: home,
		noColor:  noColorMode(plain, os.Getenv("NO_COLOR"), os.Getenv("TERM"), stdoutIsTTY()),
		modelRef: cfg.Provider.Model, opts: o, red: redact.New(redact.Options{}),
	})
	actions := &tuiActions{
		db: func(ctx context.Context) (*ctxdb.Store, error) {
			mdl, xerr := r.ensure(ctx)
			if xerr != nil {
				return nil, xerr
			}
			return mdl.db, nil
		},
		cfg: &cfg, configDir: configDir, modelRef: cfg.Provider.Model, active: r.Active,
		configHash: r.ConfigHash,
	}
	app, err := tui.NewApp(sid, &tui.Host{
		Clock: r, Runner: r, Config: r, Runtime: r, Actions: r, Shell: r,
		Sessions: actions, Files: filesAdapter{}, Clipboard: clipboardAdapter{},
	})
	if err == nil {
		benchTrace("app-built")
		prog := tea.NewProgram(app)
		bridge.setSender(prog.Send)
		benchTrace("run-start")
		_, err = prog.Run()
	}
	if cerr := r.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("close: %w", cerr))
	}
	if err != nil {
		return tuiFail(err)
	}
	return 0
}

func tuiFail(err error) int {
	if _, werr := fmt.Fprintf(os.Stderr, "tui: %s\n", cleanLine(err.Error())); werr != nil {
		return 2
	}
	return 2
}

type tuiDeps struct {
	cfg       *config.Config
	dataDir   string
	configDir string
	cfgFile   string
	noColor   bool
	home      string
	modelRef  string
	opts      *runOptions
	red       *redact.Redactor
	now       func() time.Time
	// bridge is the ACP session's approval transport (GAP-UX-01); nil on
	// the TUI path, which builds its own.
	bridge *approvalBridge
}

// tuiRunner is the Runner, Clock, and ConfigSource over the lazy loop.
type tuiRunner struct {
	ensureMu sync.Mutex
	mu       sync.Mutex
	snapMu   sync.RWMutex
	snapshot tui.Config
	cfgHash  string
	deps     tuiDeps
	model    *runModel
	loop     *loop.Loop
}

// newTUIRunner injects the same clock into loop.Config.Now (E-72).
func newTUIRunner(d *tuiDeps) *tuiRunner {
	d.now = time.Now
	r := &tuiRunner{deps: *d}
	r.snapshot = snapshotFor(d.cfg, d.modelRef)
	r.cfgHash = hashConfig(d.cfg)
	return r
}

func (r *tuiRunner) Now() time.Time { return r.deps.now() }

func (r *tuiRunner) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (r *tuiRunner) Snapshot() tui.Config {
	r.snapMu.RLock()
	defer r.snapMu.RUnlock()
	snap := r.snapshot
	snap.NoColor = r.deps.noColor
	return snap
}

func (r *tuiRunner) Prompt(ctx context.Context, sid string, in tui.Input) (<-chan tui.Update, error) {
	model, err := r.ensure(ctx)
	if err != nil {
		return nil, err
	}
	// /new and /fork switch the active session; the lazy model follows so the
	// nudge counter and close path use the session actually in use.
	model.sessionID = sid
	r.mu.Lock()
	opts := *r.deps.opts
	r.mu.Unlock()
	opts.prompt = in.Text
	opts.now = r.deps.now
	eng, err := newTurnEngine(ctx, r.deps.cfg, &opts, model, r.deps.modelRef, r.deps.red, model.spill)
	if err != nil {
		return nil, err
	}
	eng.disp.BeginTurn()
	if xerr := applyPromptRefs(ctx, eng.disp, &opts, model); xerr != nil {
		return nil, xerr
	}
	r.mu.Lock()
	r.loop = eng.loop
	r.mu.Unlock()
	stream, err := eng.loop.Prompt(ctx, sid, loop.Input{Text: opts.prompt, PlanMode: in.PlanMode})
	if err != nil {
		return nil, fmt.Errorf("tui prompt: %w", err)
	}
	out := make(chan tui.Update, 16)
	_ = boundary.Go(ctx, "tui.pump", func(ctx context.Context) error {
		pumpUpdates(ctx, stream, out, r.deps.now, func(callID string) int64 {
			return toolActionID(ctx, model.db, sid, callID)
		})
		return nil
	})
	return out, nil
}

func (r *tuiRunner) Cancel(sid string) {
	r.mu.Lock()
	lp := r.loop
	r.mu.Unlock()
	if lp != nil {
		lp.Cancel(sid)
	}
}

// SetApproval installs the approval policy for the session's next turn
// (SPEC §14.1); the ACP mode picker maps onto it.
func (r *tuiRunner) SetApproval(mode string) error {
	switch mode {
	case "ask", "auto-edit", "auto":
	default:
		return fmt.Errorf("acp: unknown approval mode %q", mode)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deps.opts == nil {
		return errors.New("acp: runner has no options")
	}
	r.deps.opts.approval = mode
	return nil
}

func (r *tuiRunner) Active(sid string) bool {
	r.mu.Lock()
	lp := r.loop
	r.mu.Unlock()
	return lp != nil && lp.Active(sid)
}

func (r *tuiRunner) Close() error {
	r.ensureMu.Lock()
	defer r.ensureMu.Unlock()
	r.mu.Lock()
	model := r.model
	r.model, r.loop = nil, nil
	r.mu.Unlock()
	if model == nil {
		return nil
	}
	return model.close(context.Background())
}

func (r *tuiRunner) ensure(ctx context.Context) (*runModel, error) {
	benchTrace("ensure-start")
	r.ensureMu.Lock()
	defer r.ensureMu.Unlock()
	r.mu.Lock()
	model := r.model
	r.mu.Unlock()
	if model != nil {
		return model, nil
	}
	codec, err := runCodec(ctx, r.deps.cfg, r.deps.dataDir, r.deps.configDir)
	if err != nil {
		return nil, err
	}
	if r.deps.opts != nil {
		r.deps.opts.configSHA = r.ConfigHash()
	}
	model, err = startRunModel(ctx, r.deps.cfg, r.deps.dataDir, r.deps.modelRef, r.deps.opts, r.deps.red, r.deps.configDir, r.deps.home, codec)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.model = model
	r.mu.Unlock()
	benchTrace("ensure-done")
	return model, nil
}

func pumpUpdates(ctx context.Context, in <-chan event.Update, out chan<- tui.Update, now func() time.Time, action func(string) int64) {
	defer close(out)
	for u := range in {
		up := translateUpdate(&u, now())
		if up.Kind == tui.KindToolResult {
			up.ActionID = action(up.ToolCallID)
		}
		select {
		case out <- up:
		case <-ctx.Done():
			return
		}
	}
}

// toolActionID resolves the journal action anchored on a tool call, so the
// block can render ↶ <action-id> (SPEC §13.1). Absent (not journaled, read
// tools, or a closed store) is 0, which renders no undo marker.
func toolActionID(ctx context.Context, db *ctxdb.Store, sid, callID string) int64 {
	if callID == "" {
		return 0
	}
	var id int64
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT a.id FROM actions a JOIN events e ON e.id = a.event_id
			WHERE e.session_id = ?1 AND e.tool_call_id = ?2 ORDER BY a.id DESC LIMIT 1`,
			[]any{sid, callID}, &id)
	})
	if err != nil {
		return 0
	}
	return id
}

func translateUpdate(u *event.Update, at time.Time) tui.Update {
	out := tui.Update{
		Kind: tui.Kind(u.Kind), Text: u.Text, ToolCallID: u.ToolCallID,
		ToolName: u.ToolName, Status: u.Status, Stop: tui.StopReason(u.Stop),
		Detail: tui.StopDetail(u.Detail), Err: u.Err, At: at,
	}
	if u.Usage != nil {
		out.Usage = &tui.Usage{
			Input: u.Usage.Input, CachedInput: u.Usage.CachedInput,
			Output: u.Usage.Output, CostUSD: u.Usage.CostUSD, Iterations: u.Usage.Iterations,
		}
	}
	return out
}

func tuiConfigPath(o *runOptions) (string, error) {
	if o.configPath != "" {
		return o.configPath, nil
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	return paths.ConfigFile(), nil
}

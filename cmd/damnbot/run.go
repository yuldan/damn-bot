// Headless one-turn runner (SPEC §10 `damnbot run`). The composition root is
// the only package allowed to import every layer (E-08).
package main

import (
	"context"
	"crypto/rand"
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
	"runtime"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/sandbox"
	"github.com/damnbot/damnbot/internal/tools"
)

type runOptions struct {
	prompt           string
	model            string
	approval         string
	unsafeShell      bool
	configPath       string
	dataDir          string
	output           string
	offline          bool
	baseURL          string
	apiKey           string
	providerAttempts int
}

func runHeadless(args []string, stdout, stderr io.Writer) int {
	opts, err := parseRunFlags(args, stderr)
	if err != nil {
		return 2
	}
	code, err := runOne(context.Background(), opts, stdout, stderr)
	if err != nil {
		if _, werr := fmt.Fprintf(stderr, "run: %v\n", err); werr != nil {
			return 2
		}
		return 2
	}
	return code
}

func parseRunFlags(args []string, stderr io.Writer) (runOptions, error) {
	var o runOptions
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.prompt, "p", "", "prompt (also accepted as positional text)")
	fs.StringVar(&o.prompt, "prompt", "", "prompt (also accepted as positional text)")
	fs.StringVar(&o.model, "model", "", "provider/model reference")
	fs.StringVar(&o.approval, "approval", "ask", "ask|auto-edit|auto")
	fs.BoolVar(&o.unsafeShell, "unsafe-auto-shell", false, "runs arbitrary shell commands chosen by the model without confirmation; use only in a disposable container")
	fs.StringVar(&o.configPath, "config", "", "config.toml path (default: profile path)")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory (default: SPEC §11)")
	fs.StringVar(&o.output, "output", "text", "text|json|stream-json")
	fs.BoolVar(&o.offline, "offline", false, "network.offline: loopback only")
	fs.StringVar(&o.baseURL, "base-url", "", "internal/test: provider base URL")
	fs.StringVar(&o.apiKey, "api-key", "", "internal/test: provider API key")
	fs.IntVar(&o.providerAttempts, "provider-attempts", 0, "internal/test: provider retry attempts (0 = default 3)")
	if err := fs.Parse(args); err != nil {
		return runOptions{}, err
	}
	if o.prompt == "" {
		o.prompt = strings.Join(fs.Args(), " ")
	}
	switch o.output {
	case "text", "json", "stream-json":
	default:
		return runOptions{}, fmt.Errorf("invalid --output %q", o.output)
	}
	if o.prompt == "" {
		return runOptions{}, errors.New("prompt required")
	}
	return o, nil
}

func runOne(ctx context.Context, o runOptions, stdout, stderr io.Writer) (int, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return 0, err
	}
	cfg, err := loadRunConfig(loader, o.configPath)
	if err != nil {
		return 0, err
	}
	paths, err := loader.Paths("")
	if err != nil {
		return 0, err
	}
	dataDir := o.dataDir
	if dataDir == "" {
		dataDir = paths.DataDir
	}
	modelRef := o.model
	if modelRef == "" {
		modelRef = cfg.Provider.Model
	}
	if modelRef == "" {
		return 0, errors.New("--model or provider.model is required")
	}
	red := redact.New(redact.Options{})
	spill := diskSpill{dir: filepath.Join(dataDir, "run-spill")}
	configDir, home := paths.ConfigDir, ""
	if h, herr := os.UserHomeDir(); herr == nil {
		home = h
	}
	model, err := startRunModel(ctx, cfg, dataDir, modelRef, o, red, spill, configDir, home)
	if err != nil {
		return 0, err
	}
	defer model.close()
	asm, err := assemble.New(assemble.Deps{
		Tokens: budgetCounter{},
		Scrub:  func(s string) string { return red.String(s) },
	})
	if err != nil {
		return 0, err
	}
	turn := asm.Freeze(&assemble.VolatileInput{
		Now:             time.Now(),
		RemainingTokens: int64(cfg.Loop.TurnMaxInputTok),
		RemainingUSD:    cfg.Budget.TurnUSD,
	})
	toolList, err := runTools(model.registry)
	if err != nil {
		return 0, err
	}
	reg := model.registry
	disp, err := tools.NewDispatcher(tools.Config{
		Registry:         reg,
		Journal:          &ctxJournal{db: model.db, sid: model.sessionID},
		Spill:            spill,
		Mode:             approvalMode(o),
		WorkspaceTrusted: true,
		Cwd:              model.cwd,
	})
	if err != nil {
		return 0, err
	}
	disp.BeginTurn()
	lp, err := loop.New(&loop.Config{
		MaxIterations:   cfg.Loop.MaxIterations,
		MaxInputTokens:  int64(cfg.Loop.TurnMaxInputTok),
		MaxOutputTokens: int64(cfg.Loop.TurnMaxOutputTok),
		MaxWall:         time.Duration(cfg.Loop.MaxWallS) * time.Second,
		MaxIdentical:    cfg.Loop.MaxIdentical,
		ParallelTools:   cfg.Loop.ParallelTools,
	}, &loop.Deps{
		Provider:  model.provider,
		Assembler: &assemblerAdapter{asm: asm, turn: turn, tools: toolList, cwd: model.cwd, model: modelRef, maxTokens: cfg.Provider.MaxOutput, store: model.store},
		Toolset:   &toolsetAdapter{disp: disp},
		Store:     model.store,
		Ledger:    model.ledger,
		Redactor:  redactorAdapter{red: red},
	})
	if err != nil {
		return 0, err
	}
	stream, err := lp.Prompt(ctx, model.sessionID, loop.Input{Text: o.prompt})
	if err != nil {
		return 0, err
	}
	result := drainRun(stream, o.output, stdout)
	result.sessionID = model.sessionID
	return writeRunResult(result, o.output, stdout), nil
}

type runModel struct {
	provider  *providerAdapter
	registry  *tools.Registry
	store     *ctxStore
	ledger    *ctxLedger
	engine    *codetools.Engine
	runner    *exec.Runner
	sessionID string
	cwd       string
	db        *ctxdb.Store
}

func (m *runModel) close() {
	if m.engine != nil {
		m.engine.Close()
	}
	if m.runner != nil {
		_ = m.runner.Close()
	}
	if m.db != nil {
		_ = m.db.Close()
	}
}

func startRunModel(ctx context.Context, cfg config.Config, dataDir, modelRef string, o runOptions, red *redact.Redactor, spill tools.Spill, configDir, home string) (*runModel, error) {
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "run"})
	if err != nil {
		return nil, err
	}
	sid, err := newSessionID()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: cwd, Model: modelRef, ConfigSHA: "f1"}); err != nil {
		_ = db.Close()
		return nil, err
	}
	roots := append([]string{cwd}, cfg.Approval.AddDirs...)
	sb, err := sandbox.New(&sandbox.Config{Roots: roots, DenyPaths: cfg.DenyPaths(configDir), Home: home})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	engine, err := codetools.New(&codetools.Config{Sandbox: sb, Roots: roots, Home: home})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	runner := exec.New(ctx)
	allow := make([]classify.AllowEntry, 0, len(cfg.Approval.ShellAllow))
	for _, argv := range cfg.Approval.ShellAllow {
		allow = append(allow, classify.AllowEntry(argv))
	}
	list := tools.FileTools(engine, sb)
	list = append(list,
		tools.NewShell(tools.ShellConfig{
			Runner: runner, Spill: spill, Redactor: red, Allow: allow, Roots: roots, Home: home,
			Cwd: cwd, DenyPaths: cfg.DenyPaths(configDir), ParentEnv: os.Environ(),
			EnvPolicy:        &exec.EnvPolicy{Allow: cfg.Exec.EnvAllow, Set: cfg.Exec.Env, PassSSHAgent: cfg.Exec.PassSSHAgent},
			WorkspaceTrusted: true, UnsafeAutoShell: o.unsafeShell, Dialect: runtimeDialect(),
		}),
		tools.NewTodo(), tools.NewClarify(nil),
	)
	reg, err := tools.NewRegistry(list...)
	if err != nil {
		engine.Close()
		_ = runner.Close()
		_ = db.Close()
		return nil, err
	}
	base, key, err := providerEndpoint(modelRef, o)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	provName, modelName, _ := strings.Cut(modelRef, "/")
	net := netx.New(netx.Config{Offline: o.offline})
	openai, err := provider.NewOpenAI(net.Client("inference", netx.Inference), provider.Config{
		Routes:   []provider.Route{{Provider: provName, Model: modelName, BaseURL: base, APIKey: key, ContextWindow: int64(cfg.Loop.TurnMaxInputTok)}},
		Attempts: o.providerAttempts,
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &runModel{
		provider:  &providerAdapter{client: openai, model: modelRef},
		registry:  reg,
		store:     &ctxStore{db: db},
		ledger:    &ctxLedger{db: db, provider: provName, model: modelName},
		sessionID: sid,
		cwd:       cwd,
		db:        db,
		engine:    engine,
		runner:    runner,
	}, nil
}

func loadRunConfig(loader *config.Loader, path string) (config.Config, error) {
	if path == "" {
		p, err := loader.Paths("")
		if err != nil {
			return config.Config{}, err
		}
		path = p.ConfigFile()
	}
	cfg, err := loader.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return config.Default(), nil
	}
	return cfg, err
}

func providerEndpoint(ref string, o runOptions) (base, key string, err error) {
	prov, _, ok := strings.Cut(ref, "/")
	if !ok {
		return "", "", fmt.Errorf("model %q is not provider/model", ref)
	}
	base = o.baseURL
	if base == "" {
		switch prov {
		case "openai":
			base = "https://api.openai.com/v1"
		case "openrouter":
			base = "https://openrouter.ai/api/v1"
		default:
			return "", "", fmt.Errorf("provider %q needs --base-url in F1", prov)
		}
	}
	key = o.apiKey
	if key == "" {
		key = os.Getenv(strings.ToUpper(prov) + "_API_KEY")
	}
	return base, key, nil
}

func approvalMode(o runOptions) tools.Mode {
	switch o.approval {
	case "auto":
		return tools.ModeHeadlessAuto
	case "auto-edit":
		return tools.ModeAutoEdit
	default:
		return tools.ModeHeadless
	}
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// budgetCounter adapts COST-09 to assemble.Counter (one formula, one owner).
type budgetCounter struct{}

func (budgetCounter) Count(s string) int { return int(budget.Count(s).Raw()) }

type providerAdapter struct {
	client *provider.OpenAI
	model  string
}

func (p *providerAdapter) Stream(ctx context.Context, req *loop.Request, emit func(loop.Delta)) (loop.Reply, error) {
	preq := &provider.Request{Model: req.Model, MaxTokens: req.MaxTokens}
	for _, m := range req.Messages {
		pm := provider.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			pm.ToolCalls = append(pm.ToolCalls, provider.ToolCall{ID: c.ID, Name: c.Name, ArgumentsRaw: c.Arguments})
		}
		preq.Messages = append(preq.Messages, pm)
	}
	for _, t := range req.Tools {
		preq.Tools = append(preq.Tools, provider.ToolSchema{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	resp, err := p.client.Do(ctx, provider.PurposeTurn, preq, func(ch provider.Chunk) {
		if len(ch.Content) > 0 {
			emit(loop.Delta{Text: string(ch.Content)})
		}
	})
	if err != nil {
		class := loop.ClassProvider
		if retry.ClassifyErr(err) == retry.ContextLength {
			class = loop.ClassContextLength
		}
		return loop.Reply{}, &loop.StreamError{Class: class, Err: err}
	}
	reply := loop.Reply{
		Content: resp.Content, FinishReason: resp.FinishReason, Usage: event.Usage{
			Input: resp.Usage.Input, CachedInput: resp.Usage.CachedInput,
			Output: resp.Usage.Output, CostUSD: resp.Usage.CostUSD,
		},
		Model: resp.Model, FallbackFrom: resp.FallbackFrom, Attempts: resp.Attempts,
	}
	for _, c := range resp.ToolCalls {
		reply.ToolCalls = append(reply.ToolCalls, loop.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.ArgumentsRaw})
	}
	return reply, nil
}

type assemblerAdapter struct {
	asm       *assemble.Assembler
	turn      *assemble.Turn
	tools     []assemble.Tool
	cwd       string
	model     string
	maxTokens int
	store     *ctxStore
}

func (a *assemblerAdapter) Assemble(ctx context.Context, req *loop.AssembleRequest) (loop.Prompt, error) {
	view := &assemble.SessionView{
		Phase:   assemble.PhaseF1,
		Context: &assemble.ContextInput{Facts: "os=" + runtime.GOOS + "; shell=headless; cwd=" + a.cwd + "; frontend=headless"},
		Tools:   a.tools,
		Turn:    a.turn,
		History: []assemble.Message{{Role: "user", Content: req.Input.Text}},
	}
	r, est, err := a.asm.Build(ctx, view)
	if err != nil {
		return loop.Prompt{}, err
	}
	out := loop.Prompt{InputTokens: int64(est.Total)}
	out.Model, out.MaxTokens = a.model, a.maxTokens
	// The system prompt and tools come from assemble; history comes from the
	// transcript so assistant tool_calls and tool results survive iterations.
	if len(r.Messages) > 0 {
		out.Messages = append(out.Messages, loop.Message{Role: r.Messages[0].Role, Content: r.Messages[0].Content})
	}
	history, err := a.store.History(ctx, req.SessionID)
	if err != nil {
		return loop.Prompt{}, err
	}
	if len(history) == 0 {
		history = []loop.Message{{Role: "user", Content: req.Input.Text}}
	}
	out.Messages = append(out.Messages, history...)
	for _, t := range r.Tools {
		out.Tools = append(out.Tools, loop.ToolSchema{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out, nil
}

func runTools(reg *tools.Registry) ([]assemble.Tool, error) {
	out := make([]assemble.Tool, 0, len(reg.Schemas()))
	for _, raw := range reg.Schemas() {
		var doc struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		out = append(out, assemble.Tool{Name: doc.Name, Description: doc.Description, Parameters: doc.Parameters})
	}
	return out, nil
}

type toolsetAdapter struct{ disp *tools.Dispatcher }

func (t *toolsetAdapter) Prepare(_ context.Context, c loop.ToolCall) (loop.Prepared, error) {
	return loop.Prepared{Call: c, Effect: loop.EffectRead, Approval: loop.ApprovalNone}, nil
}

func (t *toolsetAdapter) Dispatch(ctx context.Context, p *loop.Prepared) (loop.ToolResult, error) {
	out := t.disp.Dispatch(ctx, tools.Call{CallID: p.Call.ID, Name: p.Call.Name, Args: json.RawMessage(p.Call.Arguments)})
	res := loop.ToolResult{Content: out.Render(), Tainted: out.Tainted}
	if out.Denied {
		res.Err = &loop.ToolError{Code: loop.CodeDenied, Message: "denied"}
	}
	if out.Err != nil {
		res.Err = &loop.ToolError{Code: string(out.Err.Code), Message: out.Err.Message, Hint: out.Err.Hint}
	}
	return res, nil
}

type redactorAdapter struct{ red *redact.Redactor }

func (r redactorAdapter) Redact(s string) string { return r.red.String(s) }

type ctxStore struct{ db *ctxdb.Store }

func (s *ctxStore) Append(ctx context.Context, sid, tid string, ev *event.Event) (int64, error) {
	ids, err := s.db.Append(ctx, sid, nil, ctxdb.Event{
		Kind: string(ev.Kind), Role: ev.Role, Content: ev.Content, TurnID: tid,
		ToolCallID: ev.ToolCallID, ToolName: ev.ToolName, Payload: string(ev.Payload), Blob: ev.BlobSHA,
	})
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return ids[0], nil
}

type ctxLedger struct {
	db              *ctxdb.Store
	provider, model string
}

func (l *ctxLedger) Record(ctx context.Context, sid, tid string, eventID int64, u *event.Usage) error {
	var micros *int64
	src := "unknown"
	if u.CostUSD != nil {
		m := int64(*u.CostUSD*1e6 + 0.5)
		micros, src = &m, "provider"
	}
	return l.db.Write(ctx, func(tx *ctxdb.Tx) error {
		var eventID int64
		if err := tx.Get("SELECT id FROM events WHERE session_id = ? ORDER BY seq DESC LIMIT 1", []any{sid}, &eventID); err != nil {
			return nil // no event to anchor the ledger row yet
		}
		_, err := ledgerstore.Insert(tx, &ledgerstore.Row{
			EventID:   eventID,
			SessionID: sid, TurnID: tid, Purpose: "turn", Provider: l.provider, Model: l.model,
			InputTokens: u.Input, CachedInputTokens: u.CachedInput, OutputTokens: u.Output,
			CostMicros: micros, CostSource: src, PricingVersion: "none", Attempts: 1,
			TTFTMS: 0, CreatedAt: time.Now().UnixMilli(),
		})
		return err
	})
}

type diskSpill struct{ dir string }

func (d diskSpill) Put(_ context.Context, data []byte) (tools.BlobRef, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return tools.BlobRef{}, err
	}
	path := filepath.Join(d.dir, sha)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return tools.BlobRef{}, err
	}
	return tools.BlobRef{SHA: sha, Size: int64(len(data)), Path: path}, nil
}

func (d diskSpill) Get(_ context.Context, sha string) ([]byte, error) {
	return os.ReadFile(filepath.Join(d.dir, sha))
}

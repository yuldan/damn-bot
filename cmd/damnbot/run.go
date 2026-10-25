// Headless one-turn runner (SPEC §10 `damnbot run`). The composition root is
// the only package allowed to import every layer (E-08).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
	"github.com/damnbot/damnbot/internal/toolset"
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
	session          string
	cont             bool
	retry            bool
	noRecap          bool
	providerAttempts int
	answerSchema     string
	// eval-only seams (never set by a flag): a provider override (fake or
	// budget-gated), a per-run config override, and a memory retriever
	// factory fed the run's ctxdb for the context tier (SPEC §4.8/§18.5).
	prov         loop.Provider
	cfgOverride  *config.Config
	retrieverFor func(*ctxdb.Store) assemble.Retriever
	// validate is --answer-schema compiled (SPEC §7.2); nil = no schema.
	validate func(string) error
}

// reportedParseError marks a parse failure the flag package already wrote to
// stderr (unknown flag, bad value, -h), so runHeadless does not print it twice.
type reportedParseError struct{ err error }

func (e *reportedParseError) Error() string { return e.err.Error() }
func (e *reportedParseError) Unwrap() error { return e.err }

func runHeadless(args []string, stdout, stderr io.Writer) int {
	opts, err := parseRunFlags(args, stderr)
	if err != nil {
		if _, reported := errors.AsType[*reportedParseError](err); !reported {
			if _, werr := fmt.Fprintf(stderr, "run: %v\n", err); werr != nil {
				return 2
			}
		}
		return 2
	}
	code, err := runOne(context.Background(), &opts, stdout, stderr)
	if err != nil {
		if _, werr := fmt.Fprintf(stderr, "run: %v\n", err); werr != nil {
			return 2
		}
		if errors.Is(err, ctxdb.ErrSchemaNewer) {
			return 1 // SPEC §4.3 error table: schema_newer maps to exit 1
		}
		if errors.Is(err, ctxdb.ErrMigrationFailed) {
			return 78 // SPEC §11.1: any migration failure exits 78
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
	fs.StringVar(&o.answerSchema, "answer-schema", "", "JSON Schema the final answer must match (SPEC §7.2); invalid answers stop as invalid_answer")
	fs.BoolVar(&o.offline, "offline", false, "network.offline: loopback only")
	fs.StringVar(&o.baseURL, "base-url", "", "internal/test: provider base URL")
	fs.StringVar(&o.apiKey, "api-key", "", "internal/test: provider API key")
	fs.StringVar(&o.session, "session", "", "resume an existing session id")
	fs.BoolVar(&o.cont, "continue", false, "continue the newest session in the current directory")
	fs.BoolVar(&o.cont, "c", false, "alias for --continue")
	fs.BoolVar(&o.retry, "retry", false, "resend the last user message of the resumed session")
	fs.BoolVar(&o.noRecap, "no-recap", false, "do not print the resume recap to stderr")
	fs.IntVar(&o.providerAttempts, "provider-attempts", 0, "internal/test: provider retry attempts (0 = default 3)")
	if err := fs.Parse(args); err != nil {
		return runOptions{}, &reportedParseError{err: fmt.Errorf("parse flags: %w", err)}
	}
	if o.prompt == "" {
		o.prompt = strings.Join(fs.Args(), " ")
	}
	switch o.output {
	case "text", "json", "stream-json":
	default:
		return runOptions{}, fmt.Errorf("invalid --output %q", o.output)
	}
	if o.retry {
		if o.prompt != "" {
			return runOptions{}, errors.New("--retry resends the last message; do not pass a prompt")
		}
		if o.session == "" && !o.cont {
			o.cont = true // retry the newest session in this directory
		}
	} else if o.prompt == "" {
		return runOptions{}, errors.New("prompt required")
	}
	if o.session != "" && o.cont {
		return runOptions{}, errors.New("--session and --continue are mutually exclusive")
	}
	return o, nil
}

// warnUnavailableToolsets reports declared-but-unregistered toolset members
// once per run, so a typo in [toolsets] is visible instead of silently
// dropping tools (ADR-139). It keeps runOne's cyclomatic complexity in budget.
func warnUnavailableToolsets(stderr io.Writer, names []string) error {
	for _, name := range names {
		if _, werr := fmt.Fprintf(stderr, "run: toolset: tool %q is not available\n", name); werr != nil {
			return fmt.Errorf("toolset warning: %w", werr)
		}
	}
	return nil
}

func runOne(ctx context.Context, o *runOptions, stdout, stderr io.Writer) (code int, err error) {
	cfg, dataDir, configDir, home, err := loadRunEnv(o)
	if err != nil {
		return 0, err
	}
	modelRef := o.model
	if modelRef == "" {
		modelRef = cfg.Provider.Model
	}
	if modelRef == "" && o.session == "" && !o.cont {
		return 0, errors.New("--model or provider.model is required")
	}
	if o.answerSchema != "" {
		if o.validate, err = compileAnswerSchema(o.answerSchema); err != nil {
			return 0, err
		}
	}
	red := redact.New(redact.Options{})
	codec, err := runCodec(ctx, &cfg, dataDir, configDir)
	if err != nil {
		return 0, err
	}
	model, err := startRunModel(ctx, &cfg, dataDir, modelRef, o, red, configDir, home, codec)
	if err != nil {
		return 0, err
	}
	if werr := warnUnavailableToolsets(stderr, model.toolsets.Unavailable); werr != nil {
		return 2, nil
	}
	defer func() {
		if cerr := model.close(ctx); cerr != nil && err == nil {
			err = fmt.Errorf("close run model: %w", cerr)
		}
	}()
	if err := applyResumeNotes(ctx, o, model, red, stderr); err != nil {
		return 0, err
	}
	if modelRef == "" {
		modelRef = model.modelRef
	}
	return runTurn(ctx, &cfg, o, model, modelRef, red, model.spill, stdout)
}

// applyResumeNotes prints the resume recap and resolves --retry to the last
// user message before the new turn starts.
func applyResumeNotes(ctx context.Context, o *runOptions, model *runModel, red *redact.Redactor, stderr io.Writer) error {
	if (o.session != "" || o.cont) && !o.noRecap {
		if err := printRecap(ctx, model.db, model.sessionID, red, stderr); err != nil {
			recapPrintf(stderr, "run: recap: %v\n", err)
		}
	}
	if !o.retry {
		return nil
	}
	prompt, err := retryPrompt(ctx, model.db, model.sessionID, red, stderr)
	if err != nil {
		return fmt.Errorf("retry: %w", err)
	}
	o.prompt = prompt
	return nil
}

// loadRunEnv loads the immutable config for one run and resolves the data,
// config and home directories.

func runTurn(ctx context.Context, cfg *config.Config, o *runOptions, model *runModel, modelRef string, red *redact.Redactor, spill tools.Spill, stdout io.Writer) (int, error) {
	asm, err := assemble.New(assemble.Deps{
		Tokens: budgetCounter{},
		Scrub:  func(s string) string { return red.String(s) },
	})
	if err != nil {
		return 0, fmt.Errorf("assemble: %w", err)
	}
	turn := asm.Freeze(&assemble.VolatileInput{
		Now:             time.Now(),
		RemainingTokens: int64(cfg.Loop.TurnMaxInputTok),
		RemainingUSD:    cfg.Budget.TurnUSD,
	})
	// The context tier is rendered once and frozen for the session (P15).
	degraded := model.window > 0 && model.window < assemble.DegradedWindow
	ctxIn := contextTierInput(model, o, degraded)
	ctxTier, err := asm.Context(ctx, ctxIn)
	if err != nil {
		return 0, fmt.Errorf("assemble context: %w", err)
	}
	toolList, err := runTools(model.registry, model.toolsets.Allowed)
	if err != nil {
		return 0, err
	}
	disp, err := tools.NewDispatcher(&tools.Config{
		Registry:         model.registry,
		Journal:          loopTranscript{},
		Spill:            spill,
		Redactor:         red,
		Mode:             approvalMode(o),
		Deferred:         model.modules.deferred,
		WorkspaceTrusted: model.trusted,
		Cwd:              model.cwd,
		// P5 amendment / GAP-COST-08: the turn inline aggregate scales with
		// the route window and the session's char/token ratio.
		WindowTokens:  model.inlineCap,
		CharsPerToken: charsPerToken(o.prompt),
	})
	if err != nil {
		return 0, fmt.Errorf("tools dispatcher: %w", err)
	}
	disp.BeginTurn()
	compactAt, maxInline := 75, 0
	if degraded {
		compactAt, maxInline = 60, 2000 // PROV-02 degraded profile (SPEC §22.1)
	}
	lp, err := loop.New(&loop.Config{
		MaxIterations:   cfg.Loop.MaxIterations,
		MaxInputTokens:  int64(cfg.Loop.TurnMaxInputTok),
		MaxOutputTokens: int64(cfg.Loop.TurnMaxOutputTok),
		MaxWall:         time.Duration(cfg.Loop.MaxWallS) * time.Second,
		MaxIdentical:    cfg.Loop.MaxIdentical,
		ParallelTools:   cfg.Loop.ParallelTools,
		CompactAt:       compactAt,
		MaxInline:       maxInline,
	}, &loop.Deps{
		Provider:       model.provider,
		Assembler:      &assemblerAdapter{asm: asm, turn: turn, tools: toolList, contextTier: ctxTier.Text, model: modelRef, maxTokens: cfg.Provider.MaxOutput, window: model.window, store: model.store},
		Toolset:        &toolsetAdapter{disp: disp, allowed: model.toolsets.Allowed},
		Store:          model.store,
		Ledger:         model.ledger,
		Carriers:       &ctxCarrierStore{db: model.db},
		Redactor:       redactorAdapter{red: red},
		ValidateAnswer: o.validate,
	})
	if err != nil {
		return 0, fmt.Errorf("loop: %w", err)
	}
	stream, err := lp.Prompt(ctx, model.sessionID, loop.Input{Text: o.prompt})
	if err != nil {
		return 0, fmt.Errorf("prompt: %w", err)
	}
	result := drainRun(stream, o.output, stdout)
	result.sessionID = model.sessionID
	return writeRunResult(&result, o.output, stdout), nil
}

type runModel struct {
	provider  loop.Provider
	registry  *tools.Registry
	store     *ctxStore
	ledger    *ctxLedger
	engine    *codetools.Engine
	runner    *exec.Runner
	sessions  *exec.Sessions
	modules   runModules
	sessionID string
	cwd       string
	// modelRef is the effective provider/model, including a model inherited
	// from a resumed session (used when no --model was passed).
	modelRef  string
	window    int64  // effective model window (registry.Limits: config/registry/learned/fallback)
	winSource string // config|registry|discovery|learned|fallback
	toolsets  toolset.Selection
	db        *ctxdb.Store
	// inlineCap is the route window budget in tokens the turn inline aggregate
	// scales with (SPEC P5 amendment, GAP-COST-08); tools.TurnInlineCap turns
	// it into the character cap with the session's char/token ratio.
	inlineCap int
	spill     tools.Spill
	// trusted is the workspace trust decision (SPEC §14) and reader reads the
	// workspace context files; both come from the wiring that opened the
	// sandbox.
	trusted bool
	reader  assemble.FileReader
}

func (m *runModel) close(ctx context.Context) error {
	if m.engine != nil {
		m.engine.Close()
	}
	var err error
	err = stopModules(ctx, &m.modules, err)
	if m.sessions != nil {
		if cerr := m.sessions.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close sessions: %w", cerr))
		}
	}
	if m.runner != nil {
		if cerr := m.runner.CloseContext(ctx); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close runner: %w", cerr))
		}
	}
	if m.db == nil {
		return err
	}
	// The turn is over: release the lease so `damnbot undo` can take its
	// short lease immediately (SPEC §18.1, REVIEW-F2-SEC5-5).
	return closeDB(ctx, m.db, m.sessionID, err)
}

// newRunProvider builds the run provider or returns the injected one.
func newRunProvider(cfg *config.Config, db *ctxdb.Store, modelRef string, o *runOptions) (loop.Provider, error) {
	table, err := registry.DefaultPriceTable()
	if err != nil {
		return nil, fmt.Errorf("pricing table: %w", err)
	}
	return newRunProviderWithTable(cfg, db, modelRef, o, table)
}

// newRunProviderWithTable is newRunProvider with an explicit table, so a test
// can price a loopback route; the composition root always passes the embedded
// default table.
func newRunProviderWithTable(cfg *config.Config, db *ctxdb.Store, modelRef string, o *runOptions, table registry.PriceTable) (loop.Provider, error) {
	if o.prov != nil {
		return o.prov, nil
	}
	base, key, family, err := providerEndpoint(modelRef, o)
	if err != nil {
		return nil, fmt.Errorf("provider endpoint: %w", err)
	}
	guard, notes, err := newBudgetGuard(cfg, db)
	if err != nil {
		return nil, err
	}
	provName, modelName, _ := strings.Cut(modelRef, "/")
	prices, err := provider.RoutePrices(table, base, modelName)
	if err != nil {
		return nil, fmt.Errorf("pricing: %w", err)
	}
	net := netx.New(netx.Config{Offline: o.offline})
	openai, err := provider.NewOpenAI(net.Client("inference", netx.Inference), &provider.Config{
		Routes:   []provider.Route{{Provider: provName, Model: modelName, BaseURL: base, APIKey: key, ContextWindow: int64(cfg.Loop.TurnMaxInputTok), Family: family, Prices: prices}},
		Attempts: o.providerAttempts,
		Guard:    guard, OnBudget: func(d budget.Decision) { notes.observe(&d) },
	})
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	return &providerAdapter{client: openai, model: modelRef, notes: notes}, nil
}

func startRunModel(ctx context.Context, cfg *config.Config, dataDir, modelRef string, o *runOptions, red *redact.Redactor, configDir, home string, codec ctxdb.Codec) (*runModel, error) {
	w, err := newRunWiring(ctx, cfg, dataDir, modelRef, o, red, configDir, home, codec)
	if err != nil {
		return nil, err
	}
	provName, modelName, _ := strings.Cut(w.modelRef, "/")
	limits, err := registry.NewLimits(nil, nil, limitstore.Store{DB: w.db}, nil)
	if err != nil {
		return nil, w.fail(fmt.Errorf("model limits: %w", err))
	}
	win, err := limits.Window(ctx, provName, modelName)
	if err != nil {
		return nil, w.fail(fmt.Errorf("model window: %w", err))
	}
	sel, err := toolset.Resolve(cfg.Toolsets, runtime.GOOS, registryNames(w.registry))
	if err != nil {
		return nil, w.fail(fmt.Errorf("toolsets: %w", err))
	}
	withDeferredMeta(&sel, registryNames(w.registry))
	prov, err := newRunProvider(cfg, w.db, w.modelRef, o)
	if err != nil {
		return nil, w.fail(err)
	}
	return &runModel{
		provider:  prov,
		registry:  w.registry,
		store:     &ctxStore{db: w.db},
		ledger:    &ctxLedger{db: w.db, provider: provName, model: modelName},
		sessionID: w.sid,
		cwd:       w.cwd,
		modelRef:  w.modelRef,
		window:    win.ContextWindow,
		winSource: string(win.Source),
		inlineCap: w.inlineCap,
		spill:     w.spill,
		toolsets:  sel,
		trusted:   w.trusted,
		reader:    w.reader,
		db:        w.db,
		engine:    w.engine,
		runner:    w.runner,
		sessions:  w.sessions,
	}, nil
}

// newRunSession creates the headless session row and returns its id and the
// working directory it was created in.
func newRunSession(ctx context.Context, db *ctxdb.Store, modelRef string) (sid, cwd string, err error) {
	if sid, err = newSessionID(); err != nil {
		return "", "", err
	}
	if cwd, err = os.Getwd(); err != nil {
		return "", "", fmt.Errorf("getwd: %w", err)
	}
	_, err = db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: cwd, Model: modelRef, ConfigSHA: "f1"})
	if err != nil {
		return "", "", fmt.Errorf("create session: %w", err)
	}
	return sid, cwd, nil
}

// shellGate builds the shell_allow list and the payment-skill gate
// directories for one run (fail closed on a skill error).
func shellGate(ctx context.Context, cfg *config.Config, configDir, cwd string) ([]classify.AllowEntry, []string, error) {
	allow := make([]classify.AllowEntry, 0, len(cfg.Approval.ShellAllow))
	for _, argv := range cfg.Approval.ShellAllow {
		allow = append(allow, classify.AllowEntry(argv))
	}
	monetary, err := paymentSkillDirs(ctx, cfg, configDir, cwd)
	if err != nil {
		return nil, nil, err
	}
	return allow, monetary, nil
}

// closeDB releases sid's lease (a no-op when this process holds none) and
// closes db, so no start or turn path leaves a lease behind that would make
// `damnbot undo` report session_busy (REVIEW-F2-SEC5-5).
func closeDB(ctx context.Context, db *ctxdb.Store, sid string, cause error) error {
	if rerr := db.ReleaseLease(ctx, sid); rerr != nil {
		cause = errors.Join(cause, fmt.Errorf("release lease: %w", rerr))
	}
	if cerr := db.CloseContext(ctx); cerr != nil {
		return errors.Join(cause, fmt.Errorf("close ctxdb: %w", cerr))
	}
	return cause
}

func loadRunConfig(loader *config.Loader, path string) (config.Config, error) {
	if path == "" {
		p, err := loader.Paths("")
		if err != nil {
			return config.Config{}, fmt.Errorf("config paths: %w", err)
		}
		path = p.ConfigFile()
	}
	cfg, err := loader.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return config.Default(), nil
	}
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func providerEndpoint(ref string, o *runOptions) (base, key, family string, err error) {
	prov, _, ok := strings.Cut(ref, "/")
	if !ok {
		return "", "", "", fmt.Errorf("model %q is not provider/model", ref)
	}
	base = o.baseURL
	if base == "" {
		switch prov {
		case "openai":
			base = "https://api.openai.com/v1"
		case "openrouter":
			base = "https://openrouter.ai/api/v1"
		default:
			return "", "", "", fmt.Errorf("provider %q needs --base-url in F1", prov)
		}
	}
	family, err = provider.NormalizeBaseURL(base)
	if err != nil {
		return "", "", "", fmt.Errorf("base_url: %w", err)
	}
	key = o.apiKey
	if key == "" {
		key = os.Getenv(strings.ToUpper(prov) + "_API_KEY")
	}
	return base, key, family, nil
}

func approvalMode(o *runOptions) tools.Mode {
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
		return "", fmt.Errorf("session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// charsPerToken is the COST-09 char/token ratio of the run prompt, the
// session sample available before the ledger has usage rows. Zero means
// unknown, which keeps the fixed P5 aggregate (GAP-COST-08).
func charsPerToken(s string) float64 {
	n, raw := utf8.RuneCountInString(s), budget.Count(s).Raw()
	if n == 0 || raw == 0 {
		return 0
	}
	return float64(n) / float64(raw)
}

func (p *providerAdapter) Stream(ctx context.Context, req *loop.Request, emit func(loop.Delta)) (loop.Reply, error) {
	preq := &provider.Request{Model: req.Model, MaxTokens: req.MaxTokens}
	for _, m := range req.Messages {
		pm := provider.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Carriers: providerCarriers(m.Carriers)}
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
	warnings := p.notes.take()
	maxTok := int64(req.MaxTokens)
	if maxTok <= 0 {
		maxTok = provider.DefaultMaxTokens
	}
	billed := billedUsage(&resp, maxTok)
	if err != nil {
		se := streamError(err)
		se.Billed = resp.BilledFailed
		se.Usage = billed
		return loop.Reply{}, se
	}
	reply := loop.Reply{
		Content: resp.Content, FinishReason: resp.FinishReason, Usage: event.Usage{
			Input: resp.Usage.Input, CachedInput: resp.Usage.CachedInput,
			Output: resp.Usage.Output, CostUSD: resp.Usage.CostUSD,
			Price: eventPrice(resp.Price),
		},
		Model: resp.Model, FallbackFrom: resp.FallbackFrom, Attempts: resp.Attempts,
		Carriers: eventCarriers(resp.Carriers), RejectedCarriers: eventCarriers(resp.RejectedCarriers),
		CarrierDrops: eventDrops(resp.CarrierDrops), ReasoningText: resp.ReasoningText,
		BudgetWarnings: warnings,
		BilledFailed:   resp.BilledFailed,
		BilledUsage:    billed,
	}
	for _, c := range resp.ToolCalls {
		reply.ToolCalls = append(reply.ToolCalls, loop.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.ArgumentsRaw})
	}
	return reply, nil
}

type assemblerAdapter struct {
	asm         *assemble.Assembler
	turn        *assemble.Turn
	tools       []assemble.Tool
	contextTier string // frozen per session (P15)
	model       string
	maxTokens   int
	window      int64 // effective model window (registry.Limits); < assemble.DegradedWindow = degraded and the loop guard budget (0 = unknown)
	store       *ctxStore
}

func (a *assemblerAdapter) Assemble(ctx context.Context, req *loop.AssembleRequest) (loop.Prompt, error) {
	view := &assemble.SessionView{
		Phase:    assemble.PhaseF1,
		Context:  a.contextTier,
		Tools:    a.tools,
		Turn:     a.turn,
		Degraded: a.window > 0 && a.window < assemble.DegradedWindow,
		History:  []assemble.Message{{Role: "user", Content: req.Input.Text}},
	}
	r, est, err := a.asm.Build(ctx, view)
	if err != nil {
		return loop.Prompt{}, fmt.Errorf("assemble build: %w", err)
	}
	out := loop.Prompt{InputTokens: int64(est.Total), WindowTokens: a.window}
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

type redactorAdapter struct{ red *redact.Redactor }

func (r redactorAdapter) Redact(s string) string { return r.red.String(s) }

type ctxStore struct{ db *ctxdb.Store }

func (s *ctxStore) Append(ctx context.Context, sid, tid string, ev *event.Event) (int64, error) {
	ids, err := s.db.Append(ctx, sid, nil, ctxdb.Event{
		Kind: string(ev.Kind), Role: ev.Role, Content: ev.Content, TurnID: tid,
		ToolCallID: ev.ToolCallID, ToolName: ev.ToolName, Payload: string(ev.Payload), Blob: ev.BlobSHA,
	})
	if err != nil {
		return 0, fmt.Errorf("append event: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return ids[0], nil
}

// BeginTurn persists turn_start with the turn_journal row in one
// transaction (SEC11-2): a kill -9 turn is then recoverable.
func (s *ctxStore) BeginTurn(ctx context.Context, sid, tid string) error {
	if err := s.db.Write(ctx, func(tx *ctxdb.Tx) error { return tx.StartTurn(sid, tid) }); err != nil {
		return fmt.Errorf("begin turn: %w", err)
	}
	return nil
}

// EndTurn persists turn_end with the loop's payload and clears the journal
// row in one transaction.
func (s *ctxStore) EndTurn(ctx context.Context, sid, tid string, payload json.RawMessage) error {
	if err := s.db.Write(ctx, func(tx *ctxdb.Tx) error { return tx.EndTurnWith(sid, tid, string(payload)) }); err != nil {
		return fmt.Errorf("end turn: %w", err)
	}
	return nil
}

// billedUsage is the conservative per-attempt projection of a priced route
// for the ledger rows of failed attempts (SEC6-11): the estimated input plus
// max_tokens at the route prices. Nil for an unpriced route (NULL/unknown).
func billedUsage(resp *provider.Response, maxTok int64) *event.Usage {
	if resp.Price == nil {
		return nil
	}
	return &event.Usage{Input: resp.EstInputTokens, Output: maxTok, Price: eventPrice(resp.Price)}
}

// eventPrice converts the provider response price entry to the loop's wire
// type (loop/event is a leaf: it does not import budget).
func eventPrice(p *budget.Price) *event.Price {
	if p == nil {
		return nil
	}
	return &event.Price{
		InputPerMTok: p.InputPerMTok, CachedInputPerMTok: p.CachedInputPerMTok,
		OutputPerMTok: p.OutputPerMTok, Version: p.Version,
	}
}

type ctxLedger struct {
	db              *ctxdb.Store
	provider, model string
}

// Record writes one usage_ledger row through budget.Entry.Account, so the
// charge is max(table, provider) for a priced route and the provider cost
// for an unpriced one (ADR-125/128), with pricing_version from the value
// actually charged. Account rejects implausible usage; the row is then
// still written with NULL/unknown cost so the guard sees the spend and
// later calls under a monetary cap are denied (REVIEW-F2-SEC6 SEC6-8),
// while Record returns the error so the turn fails visibly.
func (l *ctxLedger) Record(ctx context.Context, sid, tid string, eventID int64, u *event.Usage) error {
	var price *budget.Price
	if u.Price != nil {
		price = &budget.Price{
			InputPerMTok: u.Price.InputPerMTok, CachedInputPerMTok: u.Price.CachedInputPerMTok,
			OutputPerMTok: u.Price.OutputPerMTok, Version: u.Price.Version,
		}
	}
	e := budget.Entry{
		SessionID: sid, TurnID: tid, EventID: eventID, Purpose: "turn",
		Provider: l.provider, Model: l.model, Attempts: 1,
		TTFTMS: 0, CreatedAt: time.Now().UnixMilli(),
	}
	acctErr := e.Account(budget.Usage{Input: u.Input, CachedInput: u.CachedInput, Output: u.Output, CostUSD: u.CostUSD}, price, 0)
	if acctErr != nil {
		// The call already happened and was paid for: record it as
		// unknown-cost with clamped token counts instead of losing it.
		e.InputTokens = max(u.Input, 0)
		e.CachedInputTokens = min(max(u.CachedInput, 0), e.InputTokens)
		e.OutputTokens = max(u.Output, 0)
		e.CostMicros, e.CostSource, e.PricingVersion = nil, string(budget.CostUnknown), budget.PricingNone
	}
	err := l.db.Write(ctx, func(tx *ctxdb.Tx) error {
		var anchor int64
		if gerr := tx.Get("SELECT id FROM events WHERE session_id = ? ORDER BY seq DESC LIMIT 1", []any{sid}, &anchor); gerr != nil {
			if errors.Is(gerr, ctxdb.ErrNotFound) {
				return nil
			}
			return fmt.Errorf("find event anchor: %w", gerr)
		}
		e.EventID = anchor
		row := ledgerstore.Row(e) // field-identical (ADR-101 butir 5)
		if _, ierr := ledgerstore.Insert(tx, &row); ierr != nil {
			return fmt.Errorf("insert ledger row: %w", ierr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("record ledger: %w", err)
	}
	if acctErr != nil {
		return fmt.Errorf("account ledger: %w", acctErr)
	}
	return nil
}

type ctxSpill struct{ db *ctxdb.Store }

func (c ctxSpill) Put(ctx context.Context, data []byte) (tools.BlobRef, error) {
	pb, err := c.db.PutBlob(data, "text/plain")
	if err != nil {
		return tools.BlobRef{}, fmt.Errorf("spill: put blob: %w", err)
	}
	if werr := c.db.Write(ctx, func(tx *ctxdb.Tx) error { return tx.Publish(pb) }); werr != nil {
		return tools.BlobRef{}, fmt.Errorf("spill: publish blob: %w", werr)
	}
	return tools.BlobRef{SHA: pb.Sha, Size: pb.Size, Path: "blob:" + pb.Sha}, nil
}

func (c ctxSpill) Get(ctx context.Context, sha string) ([]byte, error) {
	data, rerr := c.db.ReadBlob(ctx, sha)
	if rerr != nil {
		return nil, fmt.Errorf("spill: read blob %s: %w", sha, rerr)
	}
	return data, nil
}

//go:build !minimal

package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gwapi"
	"github.com/damnbot/damnbot/internal/i18n"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/obs"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/toolset"
)

// apiServerOptions is the flag set of `damnbot api-server`.
type apiServerOptions struct {
	addr                 string
	model                string
	configPath           string
	dataDir              string
	tokenSecret          string
	allowUnauthenticated bool
	allowPublicBind      bool
	bodyLimit            int64
	timeout              time.Duration
	baseURL              string
	apiKey               string
	offline              bool
}

func apiServerFlagSet(o *apiServerOptions, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("api-server", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.addr, "addr", "127.0.0.1:7877", "loopback listen address (use --allow-public-bind for anything else)")
	fs.StringVar(&o.model, "model", "", "provider/model the API serves (default: provider.model)")
	fs.StringVar(&o.configPath, "config", "", "config.toml path (default: profile path)")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory (default: SPEC §11)")
	fs.StringVar(&o.tokenSecret, "token-secret", "", "secret-store name holding the bearer token (default: DAMNBOT_API_TOKEN)")
	fs.BoolVar(&o.allowUnauthenticated, "allow-unauthenticated", false, "explicitly serve without a bearer token (loopback only)")
	fs.BoolVar(&o.allowPublicBind, "allow-public-bind", false, "accept a non-loopback --addr")
	fs.Int64Var(&o.bodyLimit, "body-limit", gwapi.DefaultBodyLimit, "maximum request body in bytes")
	fs.DurationVar(&o.timeout, "timeout", gwapi.DefaultTimeout, "per-turn deadline")
	fs.StringVar(&o.baseURL, "base-url", "", "internal/test: provider base URL")
	fs.StringVar(&o.apiKey, "api-key", "", "internal/test: provider API key")
	fs.BoolVar(&o.offline, "offline", false, "network.offline: loopback only")
	return fs
}

func apiServerUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot api-server [flags]\n")
	apiServerFlagSet(&apiServerOptions{}, w).PrintDefaults()
}

// runAPIServer is the surface entry point (F7 PLAT-028).
func runAPIServer(args []string, stdout, stderr io.Writer) int {
	var o apiServerOptions
	if err := apiServerFlagSet(&o, stderr).Parse(args); err != nil {
		return 2
	}
	code, err := apiServerRun(context.Background(), &o, stdout, stderr)
	if err != nil {
		sessionsPrintf(stderr, "api-server: %s\n", cleanLine(err.Error()))
	}
	return code
}

func apiServerRun(ctx context.Context, o *apiServerOptions, stdout, _ io.Writer) (int, error) {
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	loader, err := config.New(config.OS())
	if err != nil {
		return 1, fmt.Errorf("config deps: %w", err)
	}
	cfg, err := loadRunConfig(loader, o.configPath)
	if err != nil {
		return 1, err
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return 1, fmt.Errorf("config paths: %w", err)
	}
	if o.dataDir == "" {
		o.dataDir = paths.DataDir
	}
	modelRef := cmp.Or(strings.TrimSpace(o.model), cfg.Provider.Model)
	if modelRef == "" {
		return 2, errors.New("--model is required (provider.model is empty)")
	}
	if lerr := validateAPIListen(o); lerr != nil {
		return 2, lerr
	}
	token, err := apiServerToken(ctx, o.tokenSecret)
	if err != nil {
		return 1, err
	}
	if token == "" && !o.allowUnauthenticated {
		return 2, apiTokenError(cfg.UI.Language)
	}
	cfg.Toolsets.Active = []string{"hermes-api-server"}
	apiCfg := gwapi.Config{
		APIKey: token, AllowUnauthenticated: o.allowUnauthenticated,
		Models: []string{modelRef}, BodyLimit: o.bodyLimit, Timeout: o.timeout, MaxTokens: cfg.Provider.MaxOutput,
	}
	srv, serr := gwapi.NewServer(&apiCfg, &apiEngine{cfg: &cfg, model: modelRef, configPath: o.configPath, dataDir: o.dataDir, baseURL: o.baseURL, apiKey: o.apiKey, offline: o.offline, seen: map[string]bool{}}, apiToolbox())
	if serr != nil {
		return 2, fmt.Errorf("api server: %w", serr)
	}
	hs := &http.Server{Addr: o.addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	ln, lerr := (&net.ListenConfig{}).Listen(sigCtx, "tcp", o.addr)
	if lerr != nil {
		return 1, fmt.Errorf("listen: %w", lerr)
	}
	sessionsPrintf(stdout, "api-server listening on %s (model %s)\n", ln.Addr().String(), modelRef)
	go func() {
		<-sigCtx.Done()
		shut, cancel := context.WithTimeout(context.WithoutCancel(sigCtx), 5*time.Second)
		defer cancel()
		if serr := hs.Shutdown(shut); serr != nil {
			return
		}
	}()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return 1, fmt.Errorf("serve: %w", err)
	}
	return 0, nil
}

func apiServerToken(ctx context.Context, secretName string) (string, error) {
	if secretName != "" {
		st, err := secretsStore()
		if err != nil {
			return "", err
		}
		tok, err := st.Get(ctx, secretName)
		if err != nil {
			return "", fmt.Errorf("token secret %q: %w", secretName, err)
		}
		return strings.TrimSpace(tok), nil
	}
	return strings.TrimSpace(config.LookupSecret("DAMNBOT_API_TOKEN")), nil
}

// apiTokenError is the web API's unauthenticated-start message, localized by
// [ui].language (the F8 web UI reuses the same catalog key).
func apiTokenError(language string) error {
	return errors.New(i18n.T(i18n.Resolve(language), i18n.KeyAPIAuthRequired))
}

// apiEngine adapts gwapi turns onto the existing loop/provider pipeline.
type apiEngine struct {
	cfg        *config.Config
	model      string
	configPath string
	dataDir    string
	baseURL    string
	apiKey     string
	offline    bool
	mu         sync.Mutex
	seen       map[string]bool
}

func (e *apiEngine) Run(ctx context.Context, turn *gwapi.Turn) (<-chan gwapi.Update, error) {
	text := apiLastUserText(turn.Messages)
	o := e.optionsFor(turn, text)
	cfg, dataDir, configDir, home, err := loadRunEnv(&o)
	if err != nil {
		return nil, &gwapi.RunError{Status: 500, Type: gwapi.TypeServer, Code: "config_error", Message: "turn configuration failed"}
	}
	red := redact.New(redact.Options{})
	eng, model, terr := startAPIEngine(ctx, &cfg, &o, dataDir, configDir, home, red)
	if terr != nil {
		return nil, terr
	}
	e.mu.Lock()
	e.seen[turn.SessionID] = true
	e.mu.Unlock()
	eng.disp.BeginTurn()
	eng.disp.MarkTainted()
	meta, merr := json.Marshal(map[string]string{"origin": "api_server", "tainted": "true"})
	if merr != nil {
		meta = nil
	}
	ch, perr := eng.loop.Prompt(ctx, model.sessionID, loop.Input{Text: text, Meta: meta})
	if perr != nil {
		if cerr := model.close(ctx); cerr != nil {
			return nil, &gwapi.RunError{Status: 409, Type: gwapi.TypeConflict, Code: "turn_in_progress", Message: "a turn is already running for this session"}
		}
		return nil, &gwapi.RunError{Status: 409, Type: gwapi.TypeConflict, Code: "turn_in_progress", Message: "a turn is already running for this session"}
	}
	return pumpAPI(ctx, ch, model, red), nil
}

func (e *apiEngine) optionsFor(turn *gwapi.Turn, text string) runOptions {
	o := runOptions{
		prompt: text, model: cmp.Or(turn.Model, e.model), approval: "ask", configPath: e.configPath,
		dataDir: e.dataDir, sessionOrigin: "web", approvalOrigin: "run", cfgOverride: e.cfg,
		baseURL: e.baseURL, apiKey: e.apiKey, offline: e.offline,
	}
	e.mu.Lock()
	if e.seen[turn.SessionID] {
		o.session = turn.SessionID
	} else {
		o.newSessionID = turn.SessionID
	}
	e.mu.Unlock()
	return o
}

func startAPIEngine(ctx context.Context, cfg *config.Config, o *runOptions, dataDir, configDir, home string, red *redact.Redactor) (*turnEngine, *runModel, error) {
	codec, cerr := runCodec(ctx, cfg, dataDir, configDir)
	if cerr != nil {
		return nil, nil, &gwapi.RunError{Status: 500, Type: gwapi.TypeServer, Code: "storage_error", Message: "turn storage is unavailable"}
	}
	model, merr := startRunModel(ctx, cfg, dataDir, o.model, o, red, configDir, home, codec)
	if merr != nil {
		return nil, nil, &gwapi.RunError{Status: 404, Type: gwapi.TypeNotFound, Code: "model_not_found", Param: "model", Message: "model is not configured: " + o.model}
	}
	eng, eerr := newTurnEngine(ctx, cfg, o, model, o.model, red, model.spill)
	if eerr != nil {
		if cres := model.close(ctx); cres != nil {
			return nil, nil, &gwapi.RunError{Status: 500, Type: gwapi.TypeServer, Code: "engine_error", Message: "turn engine failed"}
		}
		return nil, nil, &gwapi.RunError{Status: 500, Type: gwapi.TypeServer, Code: "engine_error", Message: "turn engine failed"}
	}
	return eng, model, nil
}

func pumpAPI(ctx context.Context, ch <-chan event.Update, model *runModel, red *redact.Redactor) <-chan gwapi.Update {
	out := make(chan gwapi.Update, 16)
	go func() {
		defer close(out)
		defer func() {
			if cerr := model.close(context.WithoutCancel(ctx)); cerr != nil {
				return
			}
		}()
		for up := range ch {
			conv, ok := apiUpdate(&up, red)
			if !ok {
				continue
			}
			select {
			case out <- conv:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func apiLastUserText(msgs []gwapi.Message) string {
	for _, m := range slices.Backward(msgs) {
		if m.Role == "user" && m.Content != "" {
			return m.Content
		}
	}
	return ""
}

// apiUpdate maps one loop stream item onto the wire update.
func apiUpdate(up *event.Update, red *redact.Redactor) (gwapi.Update, bool) {
	switch up.Kind {
	case event.UpdateTextDelta:
		return gwapi.Update{Kind: gwapi.UpdateText, Text: up.Text}, true
	case event.UpdateToolCallEnd:
		return gwapi.Update{Kind: gwapi.UpdateToolCall, ToolCallID: up.ToolCallID, ToolName: up.ToolName, ToolArgs: up.Text}, true
	case event.UpdateToolResult:
		return gwapi.Update{Kind: gwapi.UpdateToolResult, ToolCallID: up.ToolCallID, ToolName: up.ToolName, Content: red.String(up.Text)}, true
	case event.UpdateToolCallStart, event.UpdateToolCallArgs, event.UpdateNotice:
		return gwapi.Update{}, false
	case event.UpdateUsage:
		if up.Usage == nil {
			return gwapi.Update{}, false
		}
		return gwapi.Update{Kind: gwapi.UpdateUsage, Usage: apiUsage(up.Usage)}, true
	case event.UpdateStop:
		finish, errText := apiStop(up, red)
		return gwapi.Update{Kind: gwapi.UpdateStop, FinishReason: finish, Err: errText, Usage: apiUsage(up.Usage)}, true
	default:
		return gwapi.Update{}, false
	}
}

func apiUsage(u *event.Usage) *gwapi.Usage {
	if u == nil {
		return nil
	}
	return &gwapi.Usage{PromptTokens: u.Input, CompletionTokens: u.Output, TotalTokens: u.Input + u.Output}
}

func apiStop(up *event.Update, red *redact.Redactor) (string, string) {
	switch up.Stop {
	case event.StopMaxTokens:
		return "length", ""
	case event.StopContentFiltered:
		return "content_filter", ""
	case event.StopError:
		return "stop", red.String(cmp.Or(up.Err, "provider error"))
	case event.StopApprovalDenied:
		return "stop", "tool approval denied"
	case event.StopToolError:
		return "stop", "tool error"
	case event.StopEndTurn, event.StopMaxIterations, event.StopCancelled, event.StopCancelledConfirmed:
		return "stop", ""
	default:
		return "stop", ""
	}
}

// apiToolbox exposes the hermes-api-server bundle read-only; every mutating
// tool is approval-required and therefore refused before a turn.
func apiToolbox() gwapi.Toolbox {
	readOnly := map[string]bool{
		"read_file": true, "glob": true, "grep": true, "web_search": true,
		"web_fetch": true, "skills_list": true, "skill_view": true, "session_search": true,
		"todo": true, "memory": true, "vision_analyze": true,
	}
	var specs []gwapi.ToolSpec
	for _, def := range toolset.Builtin() {
		if def.Name != "hermes-api-server" {
			continue
		}
		for _, name := range def.Tools {
			specs = append(specs, gwapi.ToolSpec{Name: name, ApprovalRequired: !readOnly[name]})
		}
	}
	return toolboxSpecs(specs)
}

type toolboxSpecs []gwapi.ToolSpec

func (t toolboxSpecs) Tools() []gwapi.ToolSpec { return t }

func validateAPIListen(o *apiServerOptions) error {
	if o.allowPublicBind {
		if _, _, err := net.SplitHostPort(o.addr); err != nil {
			return fmt.Errorf("--addr: %w", err)
		}
		return nil
	}
	if err := obs.ValidateListen(o.addr); err != nil {
		return fmt.Errorf("--addr must be loopback: %w", err)
	}
	return nil
}

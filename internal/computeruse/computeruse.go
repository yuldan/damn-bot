package computeruse

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// ToolName is the one deferred tool this module contributes (ADR-309).
const ToolName = "computer_use"

// Limits bound one action and one backend round trip.
const (
	DefaultActionTimeout = 15 * time.Second
	DefaultMaxScreenshot = 4 << 20
	MaxTextBytes         = 4096
	maxKeyLen            = 64
	maxCoordinate        = 32767
	maxScroll            = 10000
	maxWaitMS            = 30000
	maxOutput            = 64 << 10
)

// ActionKind is the closed action vocabulary (SPEC §9.4).
type ActionKind string

// ActionMove and its siblings are the closed action set.
const (
	ActionMove       ActionKind = "move"
	ActionClick      ActionKind = "click"
	ActionType       ActionKind = "type"
	ActionKey        ActionKind = "key"
	ActionScroll     ActionKind = "scroll"
	ActionScreenshot ActionKind = "screenshot"
	ActionWait       ActionKind = "wait"
)

// ReadOnly reports whether k needs no approval: screenshot, wait and move.
func (k ActionKind) ReadOnly() bool {
	return k == ActionScreenshot || k == ActionWait || k == ActionMove
}

// Action is one closed computer-use action.
type Action struct {
	Kind   ActionKind `json:"kind"`
	X      int        `json:"x,omitempty"`
	Y      int        `json:"y,omitempty"`
	DX     int        `json:"dx,omitempty"`
	DY     int        `json:"dy,omitempty"`
	Text   string     `json:"text,omitempty"`
	Key    string     `json:"key,omitempty"`
	Button string     `json:"button,omitempty"`
	WaitMS int        `json:"wait_ms,omitempty"`
}

func validKind(k ActionKind) bool {
	switch k {
	case ActionMove, ActionClick, ActionType, ActionKey, ActionScroll, ActionScreenshot, ActionWait:
		return true
	}
	return false
}

// Validate enforces the vocabulary, bounds and foreign-field rules.
func (a *Action) Validate() error {
	if !validKind(a.Kind) {
		return fmt.Errorf("%w: action %q", ErrInvalid, a.Kind)
	}
	if err := a.bounds(); err != nil {
		return err
	}
	switch a.Kind {
	case ActionMove, ActionScreenshot:
	case ActionClick:
		return a.checkClick()
	case ActionType:
		return a.checkText()
	case ActionKey:
		if !validKey(a.Key) {
			return fmt.Errorf("%w: key %q", ErrInvalid, a.Key)
		}
	case ActionScroll:
		if a.DX == 0 && a.DY == 0 {
			return fmt.Errorf("%w: empty scroll", ErrInvalid)
		}
	case ActionWait:
		if a.WaitMS <= 0 || a.WaitMS > maxWaitMS {
			return fmt.Errorf("%w: wait_ms", ErrInvalid)
		}
	}
	return nil
}

func (a *Action) bounds() error {
	if a.X < 0 || a.X > maxCoordinate || a.Y < 0 || a.Y > maxCoordinate ||
		a.DX < -maxScroll || a.DX > maxScroll || a.DY < -maxScroll || a.DY > maxScroll {
		return fmt.Errorf("%w: coordinate or scroll bound", ErrInvalid)
	}
	return nil
}

func (a *Action) checkClick() error {
	switch a.Button {
	case "", "left", "right", "middle":
		return nil
	}
	return fmt.Errorf("%w: button %q", ErrInvalid, a.Button)
}

func (a *Action) checkText() error {
	if a.Text == "" || len(a.Text) > MaxTextBytes || !utf8.ValidString(a.Text) || strings.ContainsRune(a.Text, 0) {
		return fmt.Errorf("%w: text must be 1..%d valid UTF-8 bytes", ErrInvalid, MaxTextBytes)
	}
	return nil
}

var keyChord = regexp.MustCompile(`^(?:(?:ctrl|alt|shift|meta)\+){0,3}(?:[a-z0-9]|f(?:[1-9]|1[0-9]|2[0-4])|enter|tab|escape|esc|backspace|delete|insert|home|end|pageup|pagedown|up|down|left|right|space)$`)

func validKey(key string) bool {
	return len(key) <= maxKeyLen && keyChord.MatchString(strings.ToLower(key))
}

// Result is one backend action outcome. Image is set only for screenshots.
type Result struct {
	Output string
	Image  []byte
}

// Backend is one external cua transport; Module serialises its calls.
type Backend interface {
	Start(ctx context.Context) error
	Act(ctx context.Context, a *Action) (Result, error)
	Screenshot(ctx context.Context) ([]byte, error)
	Stop(ctx context.Context) error
}

// Deps carries the core-owned capabilities this module needs.
type Deps struct {
	Spawn     func(ctx context.Context, spec host.SpawnSpec) (host.Process, error)
	Approvals host.Approvals
	Secrets   host.Secrets
}

// Config is the normalised [modules.computer_use] configuration.
type Config struct {
	Backend            string
	Endpoint           string
	Command            []string
	Env                []string
	Timeout            time.Duration
	MaxScreenshotBytes int
}

// Module is one serialised computer-use session.
type Module struct {
	cfg     Config
	deps    Deps
	backend Backend

	mu      sync.Mutex
	started bool
	enabled bool
	status  host.Status
}

// New builds the process backend; a missing command fails closed at runtime.
func New(cfg *Config, deps Deps) (*Module, error) {
	cfg.Timeout = cmp.Or(cfg.Timeout, DefaultActionTimeout)
	cfg.MaxScreenshotBytes = cmp.Or(cfg.MaxScreenshotBytes, DefaultMaxScreenshot)
	if deps.Approvals == nil {
		deps.Approvals = host.DenyApprovals{}
	}
	if deps.Secrets == nil {
		deps.Secrets = host.DenySecrets{}
	}
	if cfg.Timeout < 0 || cfg.MaxScreenshotBytes < 0 || len(cfg.Env) > 32 {
		return nil, fmt.Errorf("%w: limits", ErrInvalid)
	}
	for _, arg := range cfg.Command {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return nil, fmt.Errorf("%w: command argument", ErrInvalid)
		}
	}
	for _, name := range cfg.Env {
		if !validEnvName(name) {
			return nil, fmt.Errorf("%w: env name %q", ErrInvalid, name)
		}
	}
	var (
		backend Backend
		err     error
	)
	switch cfg.Backend {
	case "", "process":
		backend, err = newProcessBackend(cfg, deps)
	case "http":
		backend, err = newHTTPBackend(cfg)
	default:
		return nil, fmt.Errorf("%w: backend %q", ErrInvalid, cfg.Backend)
	}
	if err != nil {
		return nil, err
	}
	// New is the direct constructor: callers that reached it already opted in.
	// NewFromConfig narrows this to `[modules.computer_use].enabled`.
	return &Module{cfg: *cfg, deps: deps, backend: backend, enabled: true, status: host.StatusStopped}, nil
}

func (m *Module) timeout() time.Duration { return cmp.Or(m.cfg.Timeout, DefaultActionTimeout) }
func (m *Module) maxShot() int           { return cmp.Or(m.cfg.MaxScreenshotBytes, DefaultMaxScreenshot) }

// Start opens the backend. Calling it twice is safe.
func (m *Module) Start(ctx context.Context) error {
	if m == nil || m.backend == nil {
		return fmt.Errorf("%w: no backend configured", ErrUnavailable)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		m.status = host.StatusStopped
		return nil
	}
	if m.started {
		m.status = host.StatusReady
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, m.timeout())
	defer cancel()
	if err := m.backend.Start(sctx); err != nil {
		m.status = host.StatusStopped
		return fmt.Errorf("computeruse: start: %w", err)
	}
	m.started = true
	m.status = host.StatusReady
	return nil
}

// Status reports the lifecycle state.
func (m *Module) Status() host.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Stop releases the backend. It is safe when never started.
func (m *Module) Stop(ctx context.Context) error {
	if m == nil || m.backend == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = false
	m.status = host.StatusStopped
	if err := m.backend.Stop(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrBackend, err)
	}
	return nil
}

// Act validates, approves and executes one action. A denial runs no action.
func (m *Module) Act(ctx context.Context, callID string, a *Action) (host.Result, error) {
	res, err := m.run(ctx, callID, a)
	if err != nil {
		return host.Result{}, err
	}
	if a.Kind != ActionScreenshot {
		return host.Result{Content: cmp.Or(strings.TrimSpace(res.Output), summary(a))}, nil
	}
	att := host.Attachment{Kind: host.AttachmentImage, Name: "screenshot.png", MIME: "image/png", Data: res.Image}
	if err := att.Validate(); err != nil {
		return host.Result{}, fmt.Errorf("%w: %w", ErrBackend, err)
	}
	return host.Result{
		Content:         fmt.Sprintf("screenshot %d bytes", len(res.Image)),
		Meta:            map[string]any{"action": string(a.Kind), "bytes": len(res.Image), "backend": m.backendName()},
		UntrustedSource: "computer_use:" + m.backendName(),
		Attachments:     []host.Attachment{att},
	}, nil
}

// backendName is the configured transport; empty means the process backend.
func (m *Module) backendName() string { return cmp.Or(m.cfg.Backend, "process") }

// Screenshot captures one bounded PNG without approval; Invoke taints it.
func (m *Module) Screenshot(ctx context.Context) ([]byte, error) {
	res, err := m.run(ctx, "", &Action{Kind: ActionScreenshot})
	if err != nil {
		return nil, err
	}
	return res.Image, nil
}

func (m *Module) run(ctx context.Context, callID string, a *Action) (Result, error) {
	if err := a.Validate(); err != nil {
		return Result{}, err
	}
	if m == nil || m.backend == nil {
		return Result{}, fmt.Errorf("%w: no backend configured", ErrUnavailable)
	}
	// Availability before approval: an absent binary never prompts.
	if err := m.Start(ctx); err != nil {
		return Result{}, err
	}
	if !a.Kind.ReadOnly() {
		if err := m.approve(ctx, callID, a); err != nil {
			return Result{}, err
		}
	}
	actx, cancel := context.WithTimeout(ctx, m.timeout())
	defer cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.backend.Act(actx, a)
	if err != nil {
		if errors.Is(actx.Err(), context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("%w: %w", ErrTimeout, err)
		}
		return Result{}, fmt.Errorf("computeruse: act: %w", err)
	}
	if a.Kind == ActionScreenshot && len(res.Image) > m.maxShot() {
		return Result{}, fmt.Errorf("%w: screenshot %d > %d bytes", ErrTooLarge, len(res.Image), m.maxShot())
	}
	return res, nil
}

// Invoke is the modules.Module adapter for `computer_use`; it fails closed on
// unknown tools, unknown fields and invalid actions.
func (m *Module) Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	if m == nil || !m.enabled {
		return host.Result{}, &host.Error{Code: "module_unavailable", Message: "computer_use is not enabled: " + tool}
	}
	if tool != ToolName {
		return host.Result{}, &host.Error{Code: "unknown_tool", Message: "unknown computeruse tool: " + tool}
	}
	var a Action
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: "invalid computer_use arguments"}
	}
	res, err := m.Act(ctx, env.CallID, &a)
	if err != nil {
		return host.Result{}, hostError(err)
	}
	return res, nil
}

// approve raises one host approval request. The preview never contains screen
// content, and only allow-once permits the action.
func (m *Module) approve(ctx context.Context, callID string, a *Action) error {
	req := &host.ApprovalRequest{
		Tool: ToolName, CallID: callID, Effect: host.EffectNonIdempotent,
		Class: "computer_use:" + string(a.Kind), Preview: preview(a),
		Args: approvalArgs(a),
	}
	if err := req.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	approver := m.deps.Approvals
	if approver == nil {
		approver = host.DenyApprovals{}
	}
	answer, err := approver.Approve(ctx, req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDenied, err)
	}
	if answer != host.ApprovalAllowOnce {
		return fmt.Errorf("%w: %s", ErrDenied, req.Class)
	}
	return nil
}

func summary(a *Action) string {
	switch a.Kind {
	case ActionClick:
		return fmt.Sprintf("click at (%d, %d) %s", a.X, a.Y, cmp.Or(a.Button, "left"))
	case ActionMove:
		return fmt.Sprintf("move to (%d, %d)", a.X, a.Y)
	case ActionType:
		return "type " + strconv.Itoa(utf8.RuneCountInString(a.Text)) + " characters"
	case ActionKey:
		return "key " + a.Key
	case ActionScroll:
		return fmt.Sprintf("scroll dx=%d dy=%d", a.DX, a.DY)
	case ActionWait:
		return "wait " + strconv.Itoa(a.WaitMS) + " ms"
	case ActionScreenshot:
		return "screenshot"
	}
	return string(a.Kind)
}

func approvalArgs(a *Action) json.RawMessage {
	cp := *a
	if cp.Kind == ActionType {
		cp.Text = strconv.Itoa(utf8.RuneCountInString(cp.Text)) + " characters"
	}
	raw, err := json.Marshal(cp)
	if err != nil {
		return nil
	}
	return raw
}

func preview(a *Action) string {
	s := "computer_use " + summary(a)
	if runes := []rune(s); len(runes) > host.MaxApprovalPreview {
		return string(runes[:host.MaxApprovalPreview])
	}
	return s
}

func hostError(err error) error {
	if he, ok := errors.AsType[*host.Error](err); ok {
		return he
	}
	code := "internal"
	for _, row := range []struct {
		code string
		err  error
	}{
		{"invalid_args", ErrInvalid},
		{"denied", ErrDenied},
		{"module_unavailable", ErrUnavailable},
		{"timeout", ErrTimeout},
		{"too_large", ErrTooLarge},
	} {
		if errors.Is(err, row.err) {
			code = row.code
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	}
	return &host.Error{Code: code, Message: err.Error()}
}

// ErrInvalid and its siblings are the fail-closed sentinels.
var (
	ErrInvalid     = errors.New("computeruse: invalid action")
	ErrUnavailable = errors.New("computeruse: module unavailable")
	ErrDenied      = errors.New("computeruse: approval denied")
	ErrTimeout     = errors.New("computeruse: action timed out")
	ErrTooLarge    = errors.New("computeruse: screenshot too large")
	ErrBackend     = errors.New("computeruse: backend failure")
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validEnvName(name string) bool { return envName.MatchString(name) }

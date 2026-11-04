//go:build !minimal

package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/media/cache"
	"github.com/damnbot/damnbot/internal/media/doc"
	"github.com/damnbot/damnbot/internal/media/gen"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

var errBadConfig = errors.New("media: invalid configuration")

// Section is [modules.media] plus core budget/network facts (ADR-318).
type Section struct {
	Enabled       bool   `json:"enabled"`
	ImageBackend  string `json:"image_backend"`
	ImageEndpoint string `json:"image_endpoint"`
	ImageKeyEnv   string `json:"image_key_env"`
	VideoBackend  string `json:"video_backend"`
	VideoCreate   string `json:"video_create_url"`
	VideoStatus   string `json:"video_status_url"`
	VideoKeyEnv   string `json:"video_key_env"`
	SessionMicros int64  `json:"session_micros"`
	DailyMicros   int64  `json:"daily_micros"`
	Offline       bool   `json:"offline"`
	NoBackground  bool   `json:"no_background"`
}

// Deps is the core-owned seam of the media module.
type Deps struct {
	Approvals host.Approvals
	Secrets   host.Secrets
	// LLM is the purpose=media completer of the composition root; Vision and
	// VisionModel are its route facts. A nil or non-vision completer leaves
	// vision_analyze/video_analyze fail-closed.
	LLM         host.Completer
	Vision      bool
	VisionModel string
}

// Module is the F6 media module.
type Module struct {
	cfg     Section
	deps    Deps
	store   host.Store
	cache   *cache.Store
	tool    gen.Doer
	loop    gen.Doer
	service *gen.Service
	mu      sync.Mutex
	spendMu sync.Mutex
	status  host.Status
}

// NewFromConfig decodes one strict section and wires the host kv store.
func NewFromConfig(h host.Host, raw json.RawMessage, deps Deps) (*Module, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: empty [modules.media] section", errBadConfig)
	}
	var s Section
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%w: %w", errBadConfig, err)
	}
	m, err := New(&s, deps)
	if err != nil {
		return nil, err
	}
	if h != nil {
		if st, serr := h.Storage("module/media"); serr == nil {
			m.store = st
			if c, cerr := cache.New(st); cerr == nil {
				m.cache = c
			}
		}
	}
	return m, nil
}

// New validates the explicit backend pairing and fails closed when empty.
func New(s *Section, deps Deps) (*Module, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil config", errBadConfig)
	}
	if s.ImageBackend != "" {
		if _, ok := gen.ImageModel(s.ImageBackend); !ok || s.ImageKeyEnv == "" {
			return nil, fmt.Errorf("%w: image backend or key", errBadConfig)
		}
	}
	if s.VideoBackend != "" {
		if _, ok := gen.VideoModel(s.VideoBackend); !ok || s.VideoKeyEnv == "" ||
			s.VideoCreate == "" || s.VideoStatus == "" || !strings.Contains(s.VideoStatus, "%s") {
			return nil, fmt.Errorf("%w: video backend, endpoint or key", errBadConfig)
		}
	}
	if badEndpoint(s.ImageEndpoint) || badEndpoint(s.VideoCreate) {
		return nil, fmt.Errorf("%w: endpoint", errBadConfig)
	}
	if deps.Approvals == nil {
		deps.Approvals = host.DenyApprovals{}
	}
	if deps.Secrets == nil {
		deps.Secrets = host.DenySecrets{}
	}
	network := netx.New(netx.Config{Offline: s.Offline, NoBackground: s.NoBackground})
	m := &Module{
		cfg: *s, deps: deps, status: host.StatusStopped,
		tool: network.Client("media", netx.Tool), loop: netx.LoopbackClient(),
	}
	m.service = &gen.Service{
		Guard:    &gen.Guard{Caps: gen.Caps{SessionMicros: s.SessionMicros, DailyMicros: s.DailyMicros}, Spend: m.spent},
		Approval: deps.Approvals, Meter: m.meter,
	}
	return m, nil
}

// Start publishes the configured backends; nothing dials or spends.
func (m *Module) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("media: start: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = host.StatusStopped
	if m.cfg.Enabled {
		m.status = host.StatusReady
	}
	return nil
}

// Status reports the lifecycle state.
func (m *Module) Status() host.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Stop is idempotent; the module owns no goroutine or process.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = host.StatusStopped
	return nil
}

// Invoke dispatches one media tool with its approval and cap gate.
func (m *Module) Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	if m == nil || !m.cfg.Enabled {
		return host.Result{}, unavailable(tool)
	}
	switch tool {
	case ToolDocRead, ToolDocWrite:
		return m.doc(ctx, tool, args, env)
	case ToolImageGenerate:
		return m.image(ctx, args, env)
	case ToolVideoGenerate:
		return m.video(ctx, args, env)
	case ToolVisionAnalyze, ToolVideoAnalyze:
		return m.analyze(ctx, tool, args, env)
	case ToolXAIVideoEdit:
		return m.xaiVideo(ctx, args, env, false)
	case ToolXAIVideoExtend:
		return m.xaiVideo(ctx, args, env, true)
	}
	return host.Result{}, &host.Error{Code: "unknown_tool", Message: "unknown media tool: " + tool}
}

func (m *Module) doc(ctx context.Context, tool string, args json.RawMessage, env host.Env) (out host.Result, err error) {
	cwd := env.Cwd
	if cwd == "" {
		cwd = env.Session.Cwd()
	}
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: "workspace is not accessible"}
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("media: close workspace: %w", cerr)
		}
	}()
	if tool == ToolDocWrite {
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if derr := json.Unmarshal(args, &a); derr != nil {
			return host.Result{}, &host.Error{Code: "invalid_args", Message: derr.Error()}
		}
		preview := fmt.Sprintf("doc_write %s (%d bytes)", a.Path, len(a.Content))
		if aerr := m.approve(ctx, tool, "media:doc_write", preview, env, args); aerr != nil {
			return host.Result{}, aerr
		}
	}
	res, err := Invoke(ctx, root, tool, args, Env{Approved: true})
	if err != nil {
		return host.Result{}, mediaError(err)
	}
	out = host.Result{Content: res.Content, Meta: res.Meta}
	if tool == ToolDocRead {
		out.UntrustedSource = "media:doc"
	}
	return out, nil
}

func (m *Module) image(ctx context.Context, args json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		Prompt string `json:"prompt"`
		Size   string `json:"size"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: err.Error()}
	}
	if a.Size == "" {
		a.Size = "1024x1024"
	}
	model, backend, err := m.imageBackend()
	if err != nil {
		return host.Result{}, err
	}
	price, ok := backend.Price(model)
	if !ok {
		return host.Result{}, unavailable(ToolImageGenerate)
	}
	estimate, err := price.EstimateImage(a.Size, a.Prompt)
	if err != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: err.Error()}
	}
	preview := fmt.Sprintf("%s %s %s: estimated $%.4f", backend.Name(), model, a.Size, float64(estimate)/1e6)
	if aerr := m.approve(ctx, ToolImageGenerate, "media:image:"+backend.Name(), preview, env, args); aerr != nil {
		return host.Result{}, aerr
	}
	res, err := m.service.Image(ctx, backend, &gen.ImageRequest{
		SessionID: env.Session.SessionID(), CallID: env.CallID,
		Model: model, Prompt: a.Prompt, Size: a.Size,
	})
	if err != nil {
		return host.Result{}, genError(err)
	}
	return host.Result{
		Content:         fmt.Sprintf("generated %s %s image (%d bytes)", res.Backend, res.Model, len(res.Data)),
		Meta:            map[string]any{"backend": res.Backend, "model": res.Model, "url": res.URL, "bytes": len(res.Data)},
		UntrustedSource: "media:image/" + res.Backend,
	}, nil
}

func (m *Module) video(ctx context.Context, args json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		Prompt    string `json:"prompt"`
		DurationS int    `json:"duration_s"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: err.Error()}
	}
	if a.DurationS == 0 {
		a.DurationS = 4
	}
	model, backend, err := m.videoBackend()
	if err != nil {
		return host.Result{}, err
	}
	res, err := m.service.Video(ctx, backend, &gen.VideoRequest{
		SessionID: env.Session.SessionID(), CallID: env.CallID,
		Model: model, Prompt: a.Prompt, DurationS: a.DurationS,
	})
	if err != nil {
		return host.Result{}, genError(err)
	}
	return host.Result{
		Content:         fmt.Sprintf("generated %s %s video", res.Backend, res.Model),
		Meta:            map[string]any{"backend": res.Backend, "model": res.Model, "url": res.URL, "mime": res.MIME},
		UntrustedSource: "media:video/" + res.Backend,
	}, nil
}

func (m *Module) xaiVideo(ctx context.Context, args json.RawMessage, env host.Env, extend bool) (host.Result, error) {
	var a struct {
		Prompt    string `json:"prompt"`
		VideoURL  string `json:"video_url"`
		DurationS int    `json:"duration_s"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: err.Error()}
	}
	tool := ToolXAIVideoEdit
	if extend {
		tool = ToolXAIVideoExtend
		if a.DurationS == 0 {
			a.DurationS = 6
		}
		if err := gen.ValidateExtendDuration(a.DurationS); err != nil {
			return host.Result{}, genError(err)
		}
	}
	if a.Prompt == "" || a.VideoURL == "" {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: tool + " needs prompt and video_url"}
	}
	if err := gen.ValidateVideoSource(a.VideoURL); err != nil {
		return host.Result{}, genError(err)
	}
	model, backend, err := m.xaiEditor(tool)
	if err != nil {
		return host.Result{}, err
	}
	res, err := m.service.EditVideo(ctx, backend, &gen.VideoRequest{
		SessionID: env.Session.SessionID(), CallID: env.CallID,
		Model: model, Prompt: a.Prompt, DurationS: a.DurationS, SourceURL: a.VideoURL,
	}, extend)
	if err != nil {
		return host.Result{}, genError(err)
	}
	verb := "edited"
	if extend {
		verb = "extended"
	}
	return host.Result{
		Content:         fmt.Sprintf("%s %s %s video", verb, res.Backend, res.Model),
		Meta:            map[string]any{"backend": res.Backend, "model": res.Model, "url": res.URL, "mime": res.MIME},
		UntrustedSource: "media:video/" + res.Backend,
	}, nil
}

func (m *Module) xaiEditor(tool string) (string, gen.VideoEditGen, error) {
	if m.cfg.VideoBackend != "xai" {
		return "", nil, unavailable(tool)
	}
	model, ok := gen.VideoModel(m.cfg.VideoBackend)
	if !ok {
		return "", nil, unavailable(tool)
	}
	key, err := m.secret(m.cfg.VideoKeyEnv)
	if err != nil {
		return "", nil, unavailable(tool)
	}
	backend, err := gen.NewXAIVideoEditor(m.doer(m.cfg.VideoCreate), m.cfg.VideoCreate, m.cfg.VideoStatus, key)
	if err != nil {
		return "", nil, genError(err)
	}
	return model, backend, nil
}

func (m *Module) imageBackend() (string, gen.ImageGen, error) {
	model, ok := gen.ImageModel(m.cfg.ImageBackend)
	if !ok {
		return "", nil, unavailable(ToolImageGenerate)
	}
	key, err := m.secret(m.cfg.ImageKeyEnv)
	if err != nil {
		return "", nil, unavailable(ToolImageGenerate)
	}
	backend, err := gen.NewImageBackend(m.cfg.ImageBackend, m.doer(m.cfg.ImageEndpoint), m.cfg.ImageEndpoint, key)
	if err != nil {
		return "", nil, unavailable(ToolImageGenerate)
	}
	return model, backend, nil
}

func (m *Module) videoBackend() (string, gen.VideoGen, error) {
	model, ok := gen.VideoModel(m.cfg.VideoBackend)
	if !ok {
		return "", nil, unavailable(ToolVideoGenerate)
	}
	key, err := m.secret(m.cfg.VideoKeyEnv)
	if err != nil {
		return "", nil, unavailable(ToolVideoGenerate)
	}
	backend, err := gen.NewVideoBackend(m.cfg.VideoBackend, m.doer(m.cfg.VideoCreate),
		m.cfg.VideoCreate, m.cfg.VideoStatus, key)
	if err != nil {
		return "", nil, unavailable(ToolVideoGenerate)
	}
	return model, backend, nil
}

func (m *Module) doer(endpoint string) gen.Doer {
	if loopbackURL(endpoint) {
		return m.loop
	}
	return m.tool
}

func (m *Module) secret(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: no key", errBadConfig)
	}
	pairs, err := m.deps.Secrets.ChildEnv(host.AllowPolicy{Inherit: []string{name}})
	if err != nil {
		return "", fmt.Errorf("media: secret: %w", err)
	}
	for _, pair := range pairs {
		if k, v, ok := strings.Cut(pair, "="); ok && k == name && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("%w: key %s is empty", errBadConfig, name)
}

func (m *Module) approve(ctx context.Context, tool, class, preview string, env host.Env, args json.RawMessage) error {
	req := &host.ApprovalRequest{
		Tool: tool, CallID: env.CallID, Effect: host.EffectNonIdempotent,
		Class: class, Preview: preview, Args: args,
	}
	if err := req.Validate(); err != nil {
		return &host.Error{Code: "denied", Message: err.Error()}
	}
	answer, err := m.deps.Approvals.Approve(ctx, req)
	if err != nil {
		return &host.Error{Code: "denied", Message: err.Error()}
	}
	if answer != host.ApprovalAllowOnce {
		return &host.Error{Code: "denied", Message: "approval denied for " + tool}
	}
	return nil
}

func (m *Module) spent(ctx context.Context, from, to time.Time) (int64, error) {
	if m.store == nil {
		return 0, fmt.Errorf("%w: no spend store", gen.ErrBudget)
	}
	m.spendMu.Lock()
	defer m.spendMu.Unlock()
	keys, err := m.store.List(ctx, []byte("spend/day/"))
	if err != nil {
		return 0, fmt.Errorf("media: spend list: %w", err)
	}
	fromDay, toDay := from.Format("2006-01-02"), to.Format("2006-01-02")
	var total int64
	for _, key := range keys {
		day := strings.TrimPrefix(string(key), "spend/day/")
		if day < fromDay || day >= toDay {
			continue
		}
		raw, ok, gerr := m.store.Get(ctx, key)
		if gerr != nil || !ok {
			continue
		}
		if n, perr := strconv.ParseInt(string(raw), 10, 64); perr == nil {
			total += n
		}
	}
	return total, nil
}

func (m *Module) meter(ctx context.Context, _ string, micros int64) error {
	if m.store == nil {
		return fmt.Errorf("media: no spend store")
	}
	if micros <= 0 {
		return nil
	}
	key := []byte("spend/day/" + time.Now().UTC().Format("2006-01-02"))
	m.spendMu.Lock()
	defer m.spendMu.Unlock()
	var used int64
	if raw, ok, err := m.store.Get(ctx, key); err != nil {
		return fmt.Errorf("media: spend get: %w", err)
	} else if ok {
		if n, perr := strconv.ParseInt(string(raw), 10, 64); perr == nil {
			used = n
		}
	}
	if err := m.store.Put(ctx, key, []byte(strconv.FormatInt(used+micros, 10))); err != nil {
		return fmt.Errorf("media: spend put: %w", err)
	}
	return nil
}

func unavailable(tool string) error {
	return &host.Error{Code: "module_unavailable", Message: "media module is unavailable: " + tool}
}

func mediaError(err error) error {
	var de *doc.Error
	if !errors.As(err, &de) {
		return &host.Error{Code: "internal", Message: err.Error()}
	}
	code := string(de.Code)
	if code == "unsupported" || code == "no_text_layer" {
		code = "invalid_args"
	}
	return &host.Error{Code: code, Message: de.Message, Hint: de.Hint}
}

func genError(err error) error {
	switch {
	case errors.Is(err, gen.ErrBudget):
		return &host.Error{Code: "budget_exhausted", Message: err.Error(), Hint: "raise budget.tools_session_usd or budget.tools_daily_usd"}
	case errors.Is(err, gen.ErrApprovalDenied):
		return &host.Error{Code: "denied", Message: err.Error()}
	case errors.Is(err, gen.ErrUncertain):
		return &host.Error{Code: "network", Message: err.Error(), Hint: "vendor outcome is uncertain; no retry"}
	case errors.Is(err, gen.ErrModuleUnavailable), errors.Is(err, gen.ErrCapability):
		return &host.Error{Code: "module_unavailable", Message: err.Error()}
	case errors.Is(err, gen.ErrInvalidRequest):
		return &host.Error{Code: "invalid_args", Message: err.Error()}
	default:
		return &host.Error{Code: "internal", Message: err.Error()}
	}
}

func badEndpoint(raw string) bool {
	u, err := url.Parse(strings.ReplaceAll(raw, "%s", "ID"))
	return raw != "" && (err != nil || u.Host == "" || u.User != nil ||
		(u.Scheme != "http" && u.Scheme != "https"))
}

func loopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	name := strings.ToLower(u.Hostname())
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(name)
	return err == nil && ip.Unmap().IsLoopback()
}

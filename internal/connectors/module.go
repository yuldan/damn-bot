package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
	plugcatalog "github.com/damnbot/damnbot/internal/plugins/catalog"
)

// Deps is the composition-root seam of the module.
type Deps struct {
	Secrets      SecretSource
	KnownSecrets []string
	Network      *netx.Network
	Now          func() time.Time
	GOOS         string
	Manage       *ManageDeps
}

// ManageDeps is the optional management seam; nil fails closed.
type ManageDeps struct {
	Accounts         AccountStore
	Installer        Installer
	InsecureTestNode bool
}

// Module is the F8 connectors module.
type Module struct {
	cfg       Config
	deps      Deps
	redactor  *Redactor
	registry  *Registry
	network   *netx.Network
	handlers  map[string]Handler
	accounts  AccountStore
	installer Installer
	catalog   *plugcatalog.Catalog
	status    host.Status
	mu        sync.RWMutex
}

// NewFromConfig decodes one [modules.connectors] section and builds the
// module. Nothing dials or reads a secret here (P16).
// NewFromConfig decodes one [modules.connectors] section.
func NewFromConfig(raw json.RawMessage, deps Deps) (*Module, error) {
	section, err := DecodeSection(raw)
	if err != nil {
		return nil, err
	}
	cfg, err := ConfigFrom(&section)
	if err != nil {
		return nil, err
	}
	return New(&cfg, deps), nil
}

// New validates cfg and returns a stopped module.
func New(cfg *Config, deps Deps) *Module {
	red := NewRedactor(deps.KnownSecrets...)
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.GOOS == "" {
		deps.GOOS = runtime.GOOS
	}
	m := &Module{
		cfg: *cfg, deps: deps, redactor: red, registry: NewRegistry(),
		handlers: map[string]Handler{}, status: host.StatusStopped,
	}
	if deps.Manage != nil {
		m.accounts = deps.Manage.Accounts
		m.installer = deps.Manage.Installer
	}
	if cat, err := plugcatalog.Load(); err == nil {
		m.catalog = cat
	}
	return m
}

// Start builds the enabled connectors.
func (m *Module) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("connectors: start: %w", err)
	}
	if !m.cfg.Enabled {
		m.status = host.StatusStopped
		return nil
	}
	if m.network == nil {
		m.network = m.deps.Network
		if m.network == nil {
			m.network = netx.New(netx.Config{
				Offline: m.cfg.Offline, NoBackground: m.cfg.NoBackground,
				Secrets: m.deps.KnownSecrets,
			})
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range m.registry.Names() {
		handler, err := m.build(name)
		if err != nil {
			m.status = host.StatusFailed
			return err
		}
		if handler == nil {
			continue
		}
		if err := m.registry.Attach(name, handler); err != nil {
			m.status = host.StatusFailed
			return err
		}
		m.handlers[name] = handler
	}
	m.status = host.StatusReady
	return nil
}

// Stop is idempotent and bounded.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = host.StatusStopped
	m.handlers = map[string]Handler{}
	m.registry = NewRegistry()
	return nil
}

// Status reports the lifecycle state.
func (m *Module) Status() host.Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// Invoke dispatches one approved tool call.
func (m *Module) Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	m.mu.RLock()
	ready := m.status == host.StatusReady
	m.mu.RUnlock()
	if !ready {
		return host.Result{}, &host.Error{
			Code:    codeModuleUnavailable,
			Message: "connectors module is not enabled", Hint: "set [modules.connectors].enabled = true",
		}
	}
	if tool == "manage_catalog" || tool == "manage_connections" {
		res, err := m.manage(ctx, tool, args, env)
		if err != nil {
			return host.Result{}, AsHostError(err)
		}
		return res, nil
	}
	handler, desc, ok := m.registry.HandlerFor(tool)
	if !ok || handler == nil {
		return host.Result{}, &host.Error{
			Code:    codeModuleUnavailable,
			Message: "connector for " + tool + " is not enabled",
		}
	}
	res, err := handler.Invoke(ctx, tool, args, env)
	if err != nil {
		return host.Result{}, AsHostError(err)
	}
	if res.UntrustedSource == "" {
		res.UntrustedSource = "connector:" + desc.Name
	}
	return res, nil
}

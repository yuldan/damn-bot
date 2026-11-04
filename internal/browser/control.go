//go:build !minimal

package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// EngineNames is the closed engine catalog.
func EngineNames() []string { return []string{"cdp", "camofox", "lightpanda", "cloud", "browser-use"} }

// ValidEngine reports whether name is in the catalog.
func ValidEngine(name string) bool { return slices.Contains(EngineNames(), name) }

// ControlStatus is the endpoint-free view of one browser session.
type ControlStatus struct {
	Engine, State           string
	Connected, RealProfile  bool
	RealApproved, Extension bool
	ExtensionReady          bool
}

func (s ControlStatus) String() string {
	engine, real, ext, approved := s.Engine, "off", "off", "no"
	if engine == "" {
		engine = "none"
	}
	if s.RealProfile {
		real = "on"
	}
	if s.Extension {
		ext = "on"
	}
	if s.RealApproved {
		approved = "yes"
	}
	return fmt.Sprintf("browser: engine=%s state=%s real_profile=%s approved=%s extension=%s", engine, s.State, real, approved, ext)
}

func stateOf(connected bool) string {
	if connected {
		return "connected"
	}
	return "closed"
}

// ControlStatus reports state without opening a connection.
func (m *Module) ControlStatus(env host.Env) ControlStatus {
	if m == nil {
		return ControlStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked(env)
}

func (m *Module) statusLocked(env host.Env) ControlStatus {
	engine := m.engineName()
	return ControlStatus{
		Engine: engine, State: stateOf(m.page != nil), Connected: m.page != nil,
		RealProfile: m.cfg.RealProfile, RealApproved: m.realApprovedLocked(env),
		Extension: m.extension, ExtensionReady: m.manifestReadyLocked(),
	}
}

func (m *Module) guard() error {
	if m == nil || !m.cfg.Enabled {
		return &host.Error{Code: "module_unavailable", Message: "browser module is disabled"}
	}
	return nil
}

func engineError(name string) error {
	return &host.Error{Code: "module_unavailable", Message: "unknown browser engine: " + name, Hint: "valid engines: cdp, camofox, lightpanda, cloud, browser-use"}
}

// Connect selects one catalog engine and opens its page.
func (m *Module) Connect(ctx context.Context, engine, profile string, env host.Env) (ControlStatus, error) {
	if err := m.guard(); err != nil {
		return ControlStatus{}, err
	}
	if !ValidEngine(engine) {
		return ControlStatus{}, engineError(engine)
	}
	if engine == "cdp" {
		if err := m.authorizeReal(ctx, profile, env); err != nil {
			return ControlStatus{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.closePageLocked(); err != nil {
		return ControlStatus{}, err
	}
	m.cfg.Engine = engine
	if _, err := m.open(ctx); err != nil {
		m.status = host.StatusFailed
		return ControlStatus{}, &host.Error{Code: "module_unavailable", Message: err.Error()}
	}
	m.status = host.StatusReady
	return m.statusLocked(env), nil
}

// SetEngine changes the configured engine without connecting.
func (m *Module) SetEngine(engine string, env host.Env) (ControlStatus, error) {
	if err := m.guard(); err != nil {
		return ControlStatus{}, err
	}
	if !ValidEngine(engine) {
		return ControlStatus{}, engineError(engine)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.closePageLocked(); err != nil {
		return ControlStatus{}, err
	}
	m.cfg.Engine = engine
	return m.statusLocked(env), nil
}

// ClosePage closes the current page; it is idempotent and keeps the engine.
func (m *Module) ClosePage() (ControlStatus, error) {
	if m == nil {
		return ControlStatus{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.closePageLocked(); err != nil {
		return ControlStatus{}, err
	}
	return m.statusLocked(host.Env{}), nil
}

func (m *Module) closePageLocked() error {
	if m.page == nil {
		return nil
	}
	if err := m.page.Close(); err != nil {
		return &host.Error{Code: "module_unavailable", Message: "close browser page: " + err.Error()}
	}
	m.page = nil
	return nil
}

// Extension applies status, enable or disable without widening real_profile.
func (m *Module) Extension(action string) (ControlStatus, error) {
	if m == nil {
		return ControlStatus{}, &host.Error{Code: "module_unavailable", Message: "browser module is unavailable"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	switch action {
	case "status":
	case "disable":
		m.extension = false
	case "enable":
		switch {
		case !m.cfg.ExtensionControl:
			err = &host.Error{Code: "denied", Message: "browser extension control is off"}
		case m.cfg.ExtensionManifest == "":
			err = &host.Error{Code: "module_unavailable", Message: "extension manifest is not configured"}
		default:
			if merr := checkManifest(m.cfg.ExtensionManifest); merr != nil {
				err = &host.Error{Code: "module_unavailable", Message: merr.Error()}
			} else {
				m.extension = true
			}
		}
	default:
		err = &host.Error{Code: "module_unavailable", Message: "usage: /browser extension status|enable|disable"}
	}
	return m.statusLocked(host.Env{}), err
}

func (m *Module) manifestReadyLocked() bool {
	return m.cfg.ExtensionManifest != "" && checkManifest(m.cfg.ExtensionManifest) == nil
}

type extensionManifest struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

func checkManifest(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("extension manifest is missing: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return errors.New("extension manifest is not a regular file of at most 64 KiB")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- absolute path from module config
	if err != nil {
		return fmt.Errorf("extension manifest is unreadable: %w", err)
	}
	var manifest extensionManifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return fmt.Errorf("extension manifest is invalid: %w", err)
	}
	if manifest.Name == "" || manifest.Version <= 0 {
		return errors.New("extension manifest needs a name and a positive version")
	}
	return nil
}

func (m *Module) authorizeReal(ctx context.Context, profile string, env host.Env) error {
	if !m.cfg.RealProfile {
		return &host.Error{Code: "denied", Message: "the real browser profile is opt-in"}
	}
	if profile == "" {
		profile = "default"
	}
	session := env.Session.SessionID()
	m.mu.Lock()
	granted := session != "" && m.realGrants[realGrantKey(session, profile)]
	m.mu.Unlock()
	if granted {
		return nil
	}
	approver := m.deps.Approvals
	if approver == nil {
		approver = host.DenyApprovals{}
	}
	req := &host.ApprovalRequest{Tool: "browser_connect", Effect: host.EffectNonIdempotent, Class: "browser:real_profile:" + profile, Preview: "connect the real browser profile " + profile}
	if err := req.Validate(); err != nil {
		return &host.Error{Code: "denied", Message: err.Error()}
	}
	answer, err := approver.Approve(ctx, req)
	if err != nil {
		return &host.Error{Code: "denied", Message: err.Error()}
	}
	if answer == host.ApprovalAllowSession && session != "" {
		m.mu.Lock()
		m.realGrants[realGrantKey(session, profile)] = true
		m.mu.Unlock()
		return nil
	}
	if answer == host.ApprovalAllowOnce {
		return nil
	}
	return &host.Error{Code: "denied", Message: "approval denied for the real browser profile"}
}

func realGrantKey(session, profile string) string { return session + "\x00" + profile }

func (m *Module) realApprovedLocked(env host.Env) bool {
	session := env.Session.SessionID()
	if session == "" {
		return false
	}
	prefix := session + "\x00"
	for key := range m.realGrants {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

//go:build !minimal

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/damnbot/damnbot/internal/browser"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/tui"
)

func newBrowserControl(cfg *config.Config, approvals host.Approvals) browserControl {
	if cfg == nil {
		return browserControlUnavailable{}
	}
	return &browserCtl{cfg: cfg.Modules.Browser, network: cfg.Network, approvals: approvals}
}

type browserCtl struct {
	mu        sync.Mutex
	cfg       config.ModulesBrowser
	network   config.Network
	approvals host.Approvals
	mod       *browser.Module
}

func (b *browserCtl) module(ctx context.Context) (*browser.Module, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mod != nil {
		return b.mod, nil
	}
	if !b.cfg.Enabled {
		return nil, errors.New("[modules.browser].enabled = false")
	}
	mc := browser.ModuleConfig{
		Enabled: true, Engine: b.cfg.Engine, CDPURL: b.cfg.CDPURL, CamofoxURL: b.cfg.CamofoxURL,
		LightpandaBin: b.cfg.LightpandaBin, CloudURL: b.cfg.CloudURL, AllowOrigins: b.cfg.AllowOrigins,
		LocalProfile: b.cfg.LocalProfile, RealProfile: b.cfg.RealProfile, ExtensionControl: b.cfg.ExtensionControl,
		ExtensionManifest: b.cfg.ExtensionManifest, DialogsAllow: b.cfg.DialogAllow,
		VaultUserEnv: b.cfg.VaultUserEnv, VaultPassEnv: b.cfg.VaultPasswordEnv, UseBin: b.cfg.UseBin,
		Offline: b.network.Offline, NoBackground: !b.network.Background,
	}
	mod, err := browser.New(&mc, browser.Deps{Approvals: b.approvals})
	if err != nil {
		return nil, fmt.Errorf("browser module: %w", err)
	}
	if err := mod.Start(ctx); err != nil {
		return nil, fmt.Errorf("browser start: %w", err)
	}
	b.mod = mod
	return mod, nil
}

func (b *browserCtl) BrowserStatus(ctx context.Context, sessionID string) string {
	m, err := b.module(ctx)
	if err != nil {
		return "browser: module_unavailable: " + err.Error()
	}
	status := m.ControlStatus(sessionEnv(sessionID))
	if !status.Connected && status.Engine == "" {
		return "browser: module_unavailable: no browser engine configured"
	}
	return status.String()
}

func (b *browserCtl) BrowserConnect(ctx context.Context, sessionID, engine string) string {
	m, err := b.module(ctx)
	if err != nil {
		return "browser: module_unavailable: " + err.Error()
	}
	status, err := m.Connect(ctx, engine, "", sessionEnv(sessionID))
	if err != nil {
		return surfaceError(err)
	}
	return "browser: connected " + status.String()
}

func (b *browserCtl) BrowserClose(ctx context.Context, sessionID string) string {
	b.mu.Lock()
	mod := b.mod
	b.mu.Unlock()
	if mod == nil {
		return "browser: already closed"
	}
	status, err := mod.ClosePage()
	if err == nil {
		err = mod.Stop(ctx)
	}
	if err != nil {
		return surfaceError(err)
	}
	b.mu.Lock()
	b.mod = nil
	b.mu.Unlock()
	return "browser: closed " + status.String()
}

func (b *browserCtl) BrowserEngine(ctx context.Context, sessionID, engine string) string {
	m, err := b.module(ctx)
	if err != nil {
		return "browser: module_unavailable: " + err.Error()
	}
	status, err := m.SetEngine(engine, sessionEnv(sessionID))
	if err != nil {
		return surfaceError(err)
	}
	return "browser: engine set to " + status.Engine + "; connect with /browser connect " + status.Engine
}

func sessionEnv(sessionID string) host.Env {
	if sessionID == "" {
		return host.Env{}
	}
	view, err := host.NewSessionView(sessionID, "", "surface", "", nil, nil)
	if err != nil {
		return host.Env{}
	}
	return host.Env{Session: view}
}

func surfaceError(err error) string {
	if he, ok := errors.AsType[*host.Error](err); ok {
		if he.Hint != "" {
			return fmt.Sprintf("browser: %s: %s (%s)", he.Code, he.Message, he.Hint)
		}
		return fmt.Sprintf("browser: %s: %s", he.Code, he.Message)
	}
	return "browser: module_unavailable: " + err.Error()
}

func (r *tuiRunner) tuiBrowser() browserControl {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.browserCtl == nil {
		var approvals host.Approvals
		if r.deps.bridge != nil {
			approvals = browserApprovals{bridge: r.deps.bridge}
		}
		r.browserCtl = newBrowserControl(r.deps.cfg, approvals)
	}
	return r.browserCtl
}

func (r *tuiRunner) BrowserStatus(ctx context.Context, sessionID string) string {
	return r.tuiBrowser().BrowserStatus(ctx, sessionID)
}

func (r *tuiRunner) BrowserConnect(ctx context.Context, sessionID, engine string) string {
	return r.tuiBrowser().BrowserConnect(ctx, sessionID, engine)
}

func (r *tuiRunner) BrowserClose(ctx context.Context, sessionID string) string {
	return r.tuiBrowser().BrowserClose(ctx, sessionID)
}

func (r *tuiRunner) BrowserEngine(ctx context.Context, sessionID, engine string) string {
	return r.tuiBrowser().BrowserEngine(ctx, sessionID, engine)
}

type browserApprovals struct{ bridge *approvalBridge }

func (a browserApprovals) Approve(ctx context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	decision, err := a.bridge.ask(ctx, &tui.ApprovalRequest{
		Tool: req.Tool, Args: string(req.Args), Preview: req.Preview, Effect: string(req.Effect), Terminal: true,
	})
	if err != nil {
		return host.ApprovalDeny, err
	}
	switch decision {
	case tui.ApprovalAllowOnce:
		return host.ApprovalAllowOnce, nil
	case tui.ApprovalAllowSession:
		return host.ApprovalAllowSession, nil
	case tui.ApprovalDeny:
		return host.ApprovalDeny, nil
	}
	return host.ApprovalDeny, nil
}

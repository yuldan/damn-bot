// Package builtin is the closed registry and seams of the bundled plugins
// that SPEC §9.10 maps to built-in modules: the context_engine slot, the four
// dashboard auth providers and the disk-cleanup plugin. Every row is opt-in
// (default off); a target that is not wired reports module_unavailable instead
// of starting, installing or executing anything.
package builtin

import (
	"errors"
	"fmt"
	"strings"
)

// Plugin IDs of the bundled plugins (upstream plugins/<id>/).
const (
	ContextEngineID   = "context_engine"
	DashboardAuthID   = "dashboard_auth"
	DiskCleanupID     = "disk_cleanup"
	AchievementsID    = "hermes-achievements"
	SecurityGuideID   = "security-guidance"
	HostedConnectorID = "connectors"
)

// Target is the compiled-in module a bundled plugin delegates to.
type Target string

// Targets of the bundled plugins.
const (
	TargetContextEngine   Target = "memoryproviders"
	TargetDashboardAuth   Target = "web.auth"
	TargetDiskCleanup     Target = "auto"
	TargetAchievements    Target = "web.achievements"
	TargetSecurityGuide   Target = "codetools"
	TargetHostedConnector Target = "connectors"
)

// ErrUnavailable reports a plugin whose target was not wired into this build
// (module_unavailable). It is a status: nothing was started or installed.
var ErrUnavailable = errors.New("module_unavailable")

// ErrUnknown reports an id outside the closed bundled set.
var ErrUnknown = errors.New("builtin: unknown plugin")

// Plugin is one closed registry row.
type Plugin struct {
	ID         string
	Target     Target
	DefaultOff bool
	// Providers is the closed provider list for dashboard_auth.
	Providers []string
}

// Plugins returns the bundled plugin registry in parity order.
func Plugins() []Plugin {
	return []Plugin{
		{ID: ContextEngineID, Target: TargetContextEngine, DefaultOff: true},
		{ID: DashboardAuthID, Target: TargetDashboardAuth, DefaultOff: true, Providers: authProviderNames()},
		{ID: DiskCleanupID, Target: TargetDiskCleanup, DefaultOff: true},
	}
}

// Lookup returns one bundled plugin by id.
func Lookup(id string) (Plugin, bool) {
	for _, p := range Plugins() {
		if p.ID == id {
			return p, true
		}
	}
	return Plugin{}, false
}

// Targets says which compiled-in targets this process wired. The zero value
// wires none, so every plugin reports module_unavailable.
type Targets struct {
	ContextEngine bool
	DashboardAuth bool
	DiskCleanup   bool
	Achievements  bool
	SecurityGuide bool
	Connectors    bool
}

func (t Targets) has(target Target) bool {
	switch target {
	case TargetContextEngine:
		return t.ContextEngine
	case TargetDashboardAuth:
		return t.DashboardAuth
	case TargetAchievements:
		return t.Achievements
	case TargetDiskCleanup:
		return t.DiskCleanup
	case TargetSecurityGuide:
		return t.SecurityGuide
	case TargetHostedConnector:
		return t.Connectors
	}
	return false
}

// Resolve checks that the plugin's target is wired. A missing target returns
// [ErrUnavailable]; an unknown plugin returns [ErrUnknown].
func Resolve(t Targets, id string) (Plugin, error) {
	p, ok := Lookup(id)
	if !ok {
		return Plugin{}, fmt.Errorf("%w: %q", ErrUnknown, id)
	}
	if !t.has(p.Target) {
		return Plugin{}, fmt.Errorf("%w: %s needs %s", ErrUnavailable, p.ID, p.Target)
	}
	return p, nil
}

// dashboards are the four dashboard auth providers of PLUG-016. Drain is the
// loopback token flow (the one-use session link), nous_oauth and
// oidc_self_hosted are both OIDC providers with different discovery sources.
func dashboards() []AuthProvider {
	return []AuthProvider{
		{Name: "basic", Mode: "basic"},
		{Name: "drain", Mode: "token"},
		{Name: "nous_oauth", Mode: "oidc"},
		{Name: "oidc_self_hosted", Mode: "oidc"},
	}
}

func authProviderNames() []string {
	out := make([]string, 0, len(dashboards()))
	for _, d := range dashboards() {
		out = append(out, d.Name)
	}
	return out
}

// AuthMode maps one closed provider name to the web auth mode it selects.
func AuthMode(name string) (string, error) {
	for _, d := range dashboards() {
		if d.Name == name {
			return d.Mode, nil
		}
	}
	return "", fmt.Errorf("%w: dashboard auth provider %q", ErrUnknown, strings.TrimSpace(name))
}

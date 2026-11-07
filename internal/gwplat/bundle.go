package gwplat

import (
	"fmt"
	"slices"

	"github.com/damnbot/damnbot/internal/toolset"
)

// Bundle binds one composite platform toolset to the adapter surfaces that
// may activate it (F7 P11, TSET-046..060). A bundle names a built-in toolset;
// it never owns a tool registry, so adding a bundle cannot make a tool
// auto-approved or reachable without its adapter.
type Bundle struct {
	// Toolset is the built-in toolset name in internal/toolset.
	Toolset string
	// Platforms are the adapter surfaces (gateway.PlatformNames) whose
	// activation exposes the bundle.
	Platforms []string
}

// Bundles returns the closed P11 bundle table, ordered by toolset name so the
// projection is deterministic. `wecom` carries two rows: the robot WS surface
// and the hardened callback surface.
func Bundles() []Bundle {
	return []Bundle{
		{Toolset: "hermes-bluebubbles", Platforms: []string{"bluebubbles"}},
		{Toolset: "hermes-dingtalk", Platforms: []string{"dingtalk"}},
		{Toolset: "hermes-discord", Platforms: []string{"discord"}},
		{Toolset: "hermes-email", Platforms: []string{"email"}},
		{Toolset: "hermes-feishu", Platforms: []string{"feishu"}},
		{Toolset: "hermes-matrix", Platforms: []string{"matrix"}},
		{Toolset: "hermes-mattermost", Platforms: []string{"mattermost"}},
		{Toolset: "hermes-qqbot", Platforms: []string{"qqbot"}},
		{Toolset: "hermes-signal", Platforms: []string{"signal"}},
		{Toolset: "hermes-slack", Platforms: []string{"slack"}},
		{Toolset: "hermes-sms", Platforms: []string{"sms"}},
		{Toolset: "hermes-wecom", Platforms: []string{"wecom"}},
		{Toolset: "hermes-wecom-callback", Platforms: []string{"wecom"}},
		{Toolset: "hermes-weixin", Platforms: []string{"weixin"}},
		{Toolset: "hermes-whatsapp", Platforms: []string{"whatsapp_cloud", "whatsapp_web"}},
	}
}

// BundleFor returns the first bundle bound to platform.
func BundleFor(platform string) (Bundle, bool) {
	for _, b := range Bundles() {
		if slices.Contains(b.Platforms, platform) {
			return b, true
		}
	}
	return Bundle{}, false
}

// BundlesFor returns every bundle bound to platform; wecom has two.
func BundlesFor(platform string) []Bundle {
	var out []Bundle
	for _, b := range Bundles() {
		if slices.Contains(b.Platforms, platform) {
			out = append(out, b)
		}
	}
	return out
}

// ActiveToolset returns the toolset a platform activates. ok is false when the
// adapter is not in the active set, so an inactive adapter exposes no bundle
// tools instead of the safe-looking default of "everything".
func ActiveToolset(platform string, active map[string]bool) (string, bool) {
	if !active[platform] {
		return "", false
	}
	b, ok := BundleFor(platform)
	if !ok {
		return "", false
	}
	return b.Toolset, true
}

// Selection resolves the bundle's toolset for one GOOS. An inactive adapter
// returns the zero Selection (zero tools) and no error: the activation gate
// lives here, not in the caller.
func (b Bundle) Selection(goos string, adapterActive bool, available map[string]bool) (toolset.Selection, error) {
	if !adapterActive {
		return toolset.Selection{}, nil
	}
	sel, err := toolset.Resolve(toolset.Config{Active: []string{b.Toolset}}, goos, available)
	if err != nil {
		return toolset.Selection{}, fmt.Errorf("gwplat: bundle %s: %w", b.Toolset, err)
	}
	return sel, nil
}

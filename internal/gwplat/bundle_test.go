package gwplat_test

import (
	"runtime"
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/render"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/toolset"
)

// The P11 bundle rows (TSET-046..060). Each named test proves one row: the
// toolset is a composite without its own registry, it carries only safe tools,
// and it contributes zero tools while its adapter is inactive.
func TestF7TSET046DiscordBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "discord")
}

func TestF7TSET047WhatsAppBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "whatsapp_cloud", "whatsapp_web")
	for _, platform := range []string{"whatsapp_cloud", "whatsapp_web"} {
		if b, ok := gwplat.BundleFor(platform); !ok || b.Toolset != "hermes-whatsapp" {
			t.Errorf("%s bundle = %+v (ok=%v)", platform, b, ok)
		}
	}
}

func TestF7TSET048SlackBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "slack")
	// The Slack reply path renders mrkdwn from the same AST (GAP-GW-04).
	got := render.Format(render.PlatformSlack, "**bold**", render.Limits{})
	if len(got) != 1 || got[0].Text != "*bold*" {
		t.Fatalf("slack renderer = %+v, want mrkdwn *bold*", got)
	}
}

func TestF7TSET049SignalBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "signal")
}

func TestF7TSET050BlueBubblesBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "bluebubbles")
}

func TestF7TSET051EmailBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "email")
	checkNoTool(t, "hermes-email")
}

func TestF7TSET052MattermostBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "mattermost")
}

func TestF7TSET053MatrixBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "matrix")
	caps, ok := gateway.CapsFor("matrix")
	if !ok {
		t.Fatal("matrix is not in the SEC-10 caps table")
	}
	if caps.SupportsEditStream() {
		t.Fatal("matrix edit-stream is enabled; GW-10 requires it off")
	}
}

func TestF7TSET054DingTalkBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "dingtalk")
}

func TestF7TSET055FeishuBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "feishu")
}

func TestF7TSET056WeixinBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "weixin")
	if b, ok := gwplat.BundleFor("weixin"); !ok || b.Toolset != "hermes-weixin" {
		t.Fatalf("weixin bundle = %+v (ok=%v)", b, ok)
	}
}

func TestF7TSET057QQBotBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "qqbot")
}

func TestF7TSET058WeComBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "wecom")
	rows := gwplat.BundlesFor("wecom")
	if len(rows) != 2 {
		t.Fatalf("wecom bundles = %d, want robot WS + callback", len(rows))
	}
}

func TestF7TSET059WeComCallbackBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "wecom")
	found := false
	for _, b := range gwplat.BundlesFor("wecom") {
		if b.Toolset == "hermes-wecom-callback" {
			found = true
		}
	}
	if !found {
		t.Fatal("hermes-wecom-callback is not bound to the wecom callback surface")
	}
}

func TestF7TSET060SMSBundle(t *testing.T) {
	t.Parallel()
	checkBundle(t, "sms")
	caps, ok := gateway.CapsFor("sms")
	if !ok || caps.MaxLen != 480 {
		t.Fatalf("sms caps = %+v (ok=%v), want the 480-character segment cap (GW-09)", caps, ok)
	}
}

// checkBundle proves the rows bound to every given platform.
func checkBundle(t *testing.T, platforms ...string) {
	t.Helper()
	for _, platform := range platforms {
		rows := gwplat.BundlesFor(platform)
		if len(rows) == 0 {
			t.Fatalf("%s has no bundle row", platform)
		}
		for _, b := range rows {
			checkBundleRow(t, b)
		}
		first := rows[0]
		if name, ok := gwplat.ActiveToolset(platform, map[string]bool{platform: true}); !ok || name != first.Toolset {
			t.Errorf("%s active toolset = %q (ok=%v), want %q", platform, name, ok, first.Toolset)
		}
		if _, ok := gwplat.ActiveToolset(platform, nil); ok {
			t.Errorf("%s activates a toolset while the adapter is inactive", platform)
		}
	}
}

func checkBundleRow(t *testing.T, b gwplat.Bundle) {
	t.Helper()
	def, ok := bundleDef(b.Toolset)
	if !ok {
		t.Fatalf("%s is not a built-in toolset", b.Toolset)
	}
	if len(def.Tools) != 0 {
		t.Errorf("%s owns a tool registry: %v", b.Toolset, def.Tools)
	}
	if len(def.Includes) == 0 {
		t.Errorf("%s includes nothing", b.Toolset)
	}
	for _, inc := range def.Includes {
		if _, ok := bundleDef(inc); !ok {
			t.Errorf("%s includes unknown toolset %q", b.Toolset, inc)
		}
	}
	sel, err := b.Selection(runtime.GOOS, true, toolset.DeclaredTools())
	if err != nil {
		t.Fatalf("%s selection: %v", b.Toolset, err)
	}
	if len(sel.Tools) == 0 || len(sel.Unavailable) != 0 {
		t.Fatalf("%s selection = %+v", b.Toolset, sel)
	}
	checkSafeBundle(t, b.Toolset, sel.Allowed)
	off, err := b.Selection(runtime.GOOS, false, toolset.DeclaredTools())
	if err != nil {
		t.Fatalf("%s inactive selection: %v", b.Toolset, err)
	}
	if len(off.Tools) != 0 || len(off.Allowed) != 0 {
		t.Fatalf("%s exposes %d tools while its adapter is inactive", b.Toolset, len(off.Tools))
	}
}

func checkNoTool(t *testing.T, name string) {
	t.Helper()
	if _, ok := bundleDef(name); !ok {
		t.Fatalf("%s is not a built-in toolset", name)
	}
	sel, err := toolset.Resolve(toolset.Config{Active: []string{name}}, runtime.GOOS, toolset.DeclaredTools())
	if err != nil {
		t.Fatalf("%s resolve: %v", name, err)
	}
	checkSafeBundle(t, name, sel.Allowed)
}

// checkSafeBundle enforces the safe composition: no shell, no file write, and
// never an outbound chat tool that could bypass the adapter's cold-outbound
// guard.
func checkSafeBundle(t *testing.T, name string, allowed map[string]bool) {
	t.Helper()
	for _, banned := range []string{"shell", "write_file", "edit_file", "room_send", "send_message"} {
		if allowed[banned] {
			t.Errorf("%s exposes %q", name, banned)
		}
	}
}

func bundleDef(name string) (toolset.Definition, bool) {
	for _, d := range toolset.Builtin() {
		if d.Name == name {
			return d, true
		}
	}
	return toolset.Definition{}, false
}

// TestF7BundleTable checks the closed table: every toolset exists once, every
// platform is a SEC-10 adapter name, and rows are sorted for determinism.
func TestF7BundleTable(t *testing.T) {
	t.Parallel()
	rows := gwplat.Bundles()
	if len(rows) != 15 {
		t.Fatalf("bundle rows = %d, want 15 (TSET-046..060)", len(rows))
	}
	names := make([]string, 0, len(rows))
	for _, b := range rows {
		names = append(names, b.Toolset)
		if _, ok := bundleDef(b.Toolset); !ok {
			t.Errorf("%s is not a built-in toolset", b.Toolset)
		}
		if len(b.Platforms) == 0 {
			t.Errorf("%s has no platform", b.Toolset)
		}
		for _, p := range b.Platforms {
			if !slices.Contains(gateway.PlatformNames(), p) {
				t.Errorf("%s binds unknown platform %q", b.Toolset, p)
			}
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("bundle rows are not sorted: %v", names)
	}
	if len(slices.Compact(slices.Clone(names))) != len(names) {
		t.Errorf("duplicate bundle toolset: %v", names)
	}
}

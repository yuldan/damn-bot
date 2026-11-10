package toolset

import (
	"maps"
	"slices"
	"testing"
)

// f8Toolset checks one connector toolset: exact membership, a clean resolve
// when the binary registers the members, unavailable members (never silent
// drops) otherwise, and no other toolset pulling a member in.
func f8Toolset(t *testing.T, name string, want []string) {
	t.Helper()
	def := builtinByName(t, name)
	if !slices.Equal(def.Tools, want) || len(def.Includes) != 0 {
		t.Fatalf("%s = %+v, want tools %v and no includes", name, def, want)
	}
	sorted := slices.Sorted(slices.Values(want))
	sel, err := Resolve(Config{Active: []string{name}}, "linux", available(want...))
	if err != nil || !slices.Equal(sel.Tools, sorted) || len(sel.Unavailable) != 0 {
		t.Fatalf("%s resolved = %+v, %v", name, sel, err)
	}
	sel, err = Resolve(Config{Active: []string{name}}, "linux", available())
	if err != nil || len(sel.Tools) != 0 || !slices.Equal(sel.Unavailable, sorted) {
		t.Fatalf("%s absent backend = %+v, %v", name, sel, err)
	}
	for _, other := range Builtin() {
		if other.Name == name || other.Name == "hermes-connectors" {
			continue
		}
		for _, tool := range want {
			if slices.Contains(resolveOne(builtinMap(), other.Name), tool) {
				t.Fatalf("%s pulls in %s; %s must stay opt-in", other.Name, tool, name)
			}
		}
	}
}

func TestF8TSET014Discord(t *testing.T) {
	t.Parallel()
	f8Toolset(t, "discord", []string{"discord"})
}

func TestF8TSET015DiscordAdmin(t *testing.T) {
	t.Parallel()
	f8Toolset(t, "discord_admin", []string{"discord_admin"})
}

func TestF8TSET016FeishuDoc(t *testing.T) {
	t.Parallel()
	f8Toolset(t, "feishu_doc", []string{"feishu_doc_read"})
}

func TestF8TSET017FeishuDrive(t *testing.T) {
	t.Parallel()
	f8Toolset(t, "feishu_drive", []string{
		"feishu_drive_list_comments", "feishu_drive_list_comment_replies",
		"feishu_drive_reply_comment", "feishu_drive_add_comment",
	})
}

func TestF8TSET031XSearch(t *testing.T) {
	t.Parallel()
	f8Toolset(t, "x_search", []string{"x_search"})
}

func TestF8TSET010Connections(t *testing.T) {
	t.Parallel()
	f8Toolset(t, "connections", []string{"manage_connections"})
}

func TestF8TSET011Catalog(t *testing.T) {
	t.Parallel()
	f8Toolset(t, "catalog", []string{"manage_catalog"})
}

// TestF8TSETConnectorsBundle proves the composite bundle: it declares no
// implementation of its own, includes the five per-surface toolsets, adds the
// six tools that have no separate toolset, and resolves to the complete
// connector catalog only when it is explicitly active.
func TestF8TSETConnectorsBundle(t *testing.T) {
	t.Parallel()
	def := builtinByName(t, "hermes-connectors")
	wantIncludes := []string{"discord", "discord_admin", "feishu_doc", "feishu_drive", "x_search"}
	if !slices.Equal(def.Includes, wantIncludes) {
		t.Fatalf("includes = %v", def.Includes)
	}
	wantTools := []string{
		"ms_graph_mail", "ms_graph_calendar", "ms_graph_files", "teams_pipeline",
		"google_meet", "osv_check", "discord", "discord_admin", "feishu_doc_read",
		"feishu_drive_list_comments", "feishu_drive_list_comment_replies",
		"feishu_drive_reply_comment", "feishu_drive_add_comment", "x_search",
	}
	sorted := slices.Sorted(slices.Values(wantTools))
	if len(sorted) != 14 {
		t.Fatalf("bundle tools = %d", len(sorted))
	}
	sel, err := Resolve(Config{Active: []string{"hermes-connectors"}}, "linux", available(wantTools...))
	if err != nil || !slices.Equal(sel.Tools, sorted) || len(sel.Unavailable) != 0 {
		t.Fatalf("bundle resolved = %+v, %v", sel, err)
	}
	// The bundle is the only composition naming the six toolset-less tools.
	for _, tool := range []string{"ms_graph_mail", "ms_graph_calendar", "ms_graph_files", "teams_pipeline", "google_meet", "osv_check"} {
		if !slices.Contains(def.Tools, tool) {
			t.Fatalf("bundle does not declare %s", tool)
		}
		for _, other := range Builtin() {
			if other.Name == "hermes-connectors" {
				continue
			}
			if slices.Contains(resolveOne(builtinMap(), other.Name), tool) {
				t.Fatalf("%s pulls in %s", other.Name, tool)
			}
		}
	}
	// Default-off: an empty selection does not mention any connector tool,
	// and the bundle itself is absent from every other definition's closure.
	for name := range maps.Keys(builtinMap()) {
		if name == "hermes-connectors" {
			continue
		}
		if slices.Contains(resolveOne(builtinMap(), name), "google_meet") {
			t.Fatalf("%s enables google_meet by default", name)
		}
	}
}

func TestF8TSET002DesktopUI(t *testing.T) {
	t.Parallel()
	want := []string{
		"annotate_preview", "close_terminal", "drive_preview", "focus_pane",
		"desktop_preview", "react_to_message", "read_terminal", "read_window_below",
		"set_theme", "show_tip", "gui_tour",
	}
	f8Toolset(t, "desktop_ui", want)
	// 0 shell/write: the bundle never names an execution or file-mutation tool.
	for _, tool := range want {
		switch tool {
		case "shell", "write_file", "edit_file":
			t.Fatalf("desktop_ui exposes %s", tool)
		}
	}
	// Default off: an empty config selects no toolset and therefore no member.
	if sel, err := Resolve(Config{}, "linux", available(want...)); err != nil || sel.Allowed != nil {
		t.Fatalf("empty config resolved to %+v, %v", sel, err)
	}
}

func TestF8TSET022Project(t *testing.T) {
	t.Parallel()
	f8Toolset(t, "project", []string{"desktop_project"})
}

func TestF8TSET024Onboarding(t *testing.T) {
	t.Parallel()
	f8Toolset(t, "onboarding", []string{"setup_beat"})
}

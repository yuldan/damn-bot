package toolset

import (
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

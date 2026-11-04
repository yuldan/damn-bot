package toolset

import (
	"slices"
	"testing"
)

func builtinByName(t *testing.T, name string) Definition {
	t.Helper()
	for _, def := range Builtin() {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("builtin toolset %q missing", name)
	return Definition{}
}

func TestF6TSET019ImageGen(t *testing.T) {
	t.Parallel()
	def := builtinByName(t, "image_gen")
	if !slices.Equal(def.Tools, []string{"image_generate"}) {
		t.Fatalf("image_gen tools = %v", def.Tools)
	}
	sel, err := Resolve(Config{Active: []string{"image_gen"}}, "linux", available("image_generate"))
	if err != nil || !slices.Equal(sel.Tools, []string{"image_generate"}) || len(sel.Unavailable) != 0 {
		t.Fatalf("resolved = %+v, %v", sel, err)
	}
	// The toolset is declared before the media module can register the tool:
	// it resolves as unavailable, never silently dropped.
	sel, err = Resolve(Config{Active: []string{"image_gen"}}, "linux", available())
	if err != nil || len(sel.Tools) != 0 || !slices.Equal(sel.Unavailable, []string{"image_generate"}) {
		t.Fatalf("absent backend = %+v, %v", sel, err)
	}
	if _, err := Resolve(Config{Active: []string{"image_gen"}, Disabled: []string{"video_gen"}}, "linux", available("image_generate")); err != nil {
		t.Fatalf("known toolsets rejected: %v", err)
	}
}

func TestF6TSET027VideoGen(t *testing.T) {
	t.Parallel()
	def := builtinByName(t, "video_gen")
	want := []string{"video_generate", "xai_video_edit", "xai_video_extend"}
	if !slices.Equal(def.Tools, want) {
		t.Fatalf("video_gen tools = %v", def.Tools)
	}
	sel, err := Resolve(Config{Active: []string{"video_gen"}}, "linux", available(want...))
	if err != nil || !slices.Equal(sel.Tools, want) {
		t.Fatalf("resolved = %+v, %v", sel, err)
	}
	// Default-off: no other built-in pulls the three video tools in.
	for _, other := range Builtin() {
		if other.Name == "video_gen" {
			continue
		}
		for _, tool := range want {
			if slices.Contains(resolveOne(builtinMap(), other.Name), tool) {
				t.Fatalf("%s pulls in %s; video_gen must stay opt-in", other.Name, tool)
			}
		}
	}
}

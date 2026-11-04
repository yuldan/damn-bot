package toolset

import (
	"slices"
	"testing"
)

func builtin(name string) Definition {
	for _, d := range Builtin() {
		if d.Name == name {
			return d
		}
	}
	return Definition{}
}

func TestF6TSET028Vision(t *testing.T) {
	t.Parallel()
	d := builtin("vision")
	if d.Name != "vision" || !slices.Equal(d.Tools, []string{"vision_analyze"}) {
		t.Fatalf("vision toolset = %+v", d)
	}
	sel, err := Resolve(Config{Active: []string{"vision"}}, "linux", available("vision_analyze"))
	if err != nil || !slices.Equal(sel.Tools, []string{"vision_analyze"}) || len(sel.Unavailable) != 0 {
		t.Fatalf("resolve available = %+v, %v", sel, err)
	}
	sel, err = Resolve(Config{Active: []string{"vision"}}, "linux", available())
	if err != nil || len(sel.Tools) != 0 || !slices.Equal(sel.Unavailable, []string{"vision_analyze"}) {
		t.Fatalf("resolve unavailable = %+v, %v", sel, err)
	}
}

func TestF6TSET029Video(t *testing.T) {
	t.Parallel()
	d := builtin("video")
	if d.Name != "video" || !slices.Equal(d.Tools, []string{"video_analyze"}) {
		t.Fatalf("video toolset = %+v", d)
	}
	// Default off: exactly one built-in names video_analyze, and no default
	// headless/coding composition includes it.
	owners := 0
	for _, def := range Builtin() {
		if slices.Contains(def.Tools, "video_analyze") || slices.Contains(def.Includes, "video") {
			owners++
			if def.Name != "video" {
				t.Errorf("%s exposes the default-off video toolset", def.Name)
			}
		}
	}
	if owners != 1 {
		t.Fatalf("video_analyze owners = %d, want 1", owners)
	}
	sel, err := Resolve(Config{Active: []string{"hermes-cli"}}, "linux", available("read_file", "video_analyze"))
	if err != nil || slices.Contains(sel.Tools, "video_analyze") {
		t.Fatalf("video_analyze leaked into the default toolset: %+v, %v", sel, err)
	}
}

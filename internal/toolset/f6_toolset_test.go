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

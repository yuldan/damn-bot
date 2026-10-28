package importer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestRuntimeRefs(t *testing.T) {
	t.Parallel()
	got := runtimeRefs("run python3 scripts/x.py then ffmpeg and docker; node/npm elsewhere")
	want := []string{"python3", "node", "ffmpeg", "docker"}
	if !slices.Equal(got, want) {
		t.Fatalf("runtimeRefs = %v, want %v", got, want)
	}
	if got := runtimeRefs("pythonic and noded prose"); len(got) != 0 {
		t.Fatalf("false runtime = %v", got)
	}
}

func TestImportSkillRuntimeMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "runtime")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: runtime\ndescription: Needs Python\n---\nrun python3 scripts/x.py\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	imp, sink, _ := testImporter(t)
	if _, err := imp.Import(context.Background(), root, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := sink.skills["runtime"].Runtime; !reflect.DeepEqual(got, []string{"python3"}) {
		t.Fatalf("runtime = %v", got)
	}
}

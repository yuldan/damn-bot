package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportExternalMemoryProviderWarns(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"byterover", "retaindb", "ByteRover"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("memory:\n  provider: "+provider+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("native memory still imports\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			imp, sink, _ := testImporter(t)
			report, err := imp.Import(context.Background(), root, Options{})
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if len(report.WarningsList()) != 1 || !strings.Contains(report.WarningsList()[0], "not ported") {
				t.Fatalf("warnings = %v", report.WarningsList())
			}
			if len(sink.memories) != 1 {
				t.Fatalf("native memory not imported: %+v", sink.memories)
			}
		})
	}
}

func TestImportUnknownMemoryProviderNoWarning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("memory:\n  provider: honcho\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	imp, _, _ := testImporter(t)
	report, err := imp.Import(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.WarningsList()) != 0 {
		t.Fatalf("warnings = %v", report.WarningsList())
	}
}

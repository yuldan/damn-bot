package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestF6GAPTOOL07FFmpegHint: the install hint appears only when the binary is
// absent; a host that has ffmpeg is never told to fetch it. DOC-013 is
// informational, so a missing optional binary never changes the doctor exit.
func TestF6GAPTOOL07FFmpegHint(t *testing.T) {
	t.Parallel()
	wantHint := "damnbot doctor tools fetch ffmpeg"
	missing := checkTools(&Options{LookPath: func(string) (string, error) {
		return "", errors.New("executable file not found in $PATH")
	}})
	if missing.Status != StatusOK || !strings.Contains(missing.Message, wantHint) {
		t.Fatalf("missing = %+v, want ok hint %q", missing, wantHint)
	}
	cfgDir, dataDir, cfgFile := doctorPaths(t)
	healthyDB(t, dataDir)
	rep := Run(t.Context(), &Options{
		ConfigDir: cfgDir, DataDir: dataDir, ConfigFile: cfgFile, ConfigFound: true, ProviderModel: "openai/gpt-test",
		Keyring:  func(context.Context) (bool, error) { return false, nil },
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	})
	if rep.Summary.Warn != 0 || rep.Exit != 0 {
		t.Fatalf("DOC-013 changed the exit contract: warn = %d exit = %d (%+v)", rep.Summary.Warn, rep.Exit, rep.Checks)
	}
	present := checkTools(&Options{LookPath: func(string) (string, error) { return "/opt/bin/ffmpeg", nil }})
	if present.Status != StatusOK {
		t.Fatalf("present = %+v, want ok", present)
	}
	if strings.Contains(present.Message, wantHint) || strings.Contains(present.Message, "fetch") {
		t.Fatalf("present host was told to fetch: %q", present.Message)
	}
}

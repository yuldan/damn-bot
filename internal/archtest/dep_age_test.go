package archtest

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDependencyAgeGate (GAP-INF-07, E-46..E-50): the supply-chain fixture
// fails a bump younger than 3 days and passes an older one; a labelled
// security hotfix with two approvers is the only exemption.
func TestDependencyAgeGate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := ""
		switch {
		case strings.HasSuffix(r.URL.Path, "/example.com/fresh/@v/v1.0.0.info"):
			body = `{"Version":"v1.0.0","Time":"2026-11-12T00:00:00Z"}`
		case strings.HasSuffix(r.URL.Path, "/example.com/old/@v/v1.0.0.info"):
			body = `{"Version":"v1.0.0","Time":"2026-11-01T00:00:00Z"}`
		default:
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write fixture: %v", err)
		}
	}))
	defer srv.Close()

	run := func(diff string, env ...string) (int, string) {
		t.Helper()
		file := filepath.Join(t.TempDir(), "go.mod.diff")
		if err := os.WriteFile(file, []byte(diff), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), "sh", "scripts/dep-age.sh", file) //nolint:gosec // G204: fixture path is created by the test
		cmd.Dir = repoDir()
		cmd.Env = append(os.Environ(),
			"DAMNBOT_DEP_PROXY="+srv.URL,
			"DAMNBOT_DEP_NOW=2026-11-13T00:00:00Z",
			"PATH="+os.Getenv("PATH"))
		cmd.Env = append(cmd.Env, env...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		code := 0
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run dep-age: %v", err)
		}
		return code, out.String()
	}

	diff := func(mod string) string {
		return "--- a/go.mod\n+++ b/go.mod\n@@ -1,1 +1,2 @@\n+\t" + mod + " v1.0.0\n"
	}
	if code, out := run(diff("example.com/fresh")); code == 0 || !strings.Contains(out, "DEP-AGE GAGAL") {
		t.Fatalf("fresh module: code=%d out=%q", code, out)
	}
	if code, out := run(diff("example.com/old")); code != 0 {
		t.Fatalf("old module: code=%d out=%q", code, out)
	}
	if code, out := run(diff("example.com/fresh"),
		"DAMNBOT_PR_LABELS=security-hotfix", "DAMNBOT_DEP_APPROVERS=2"); code != 0 {
		t.Fatalf("hotfix module: code=%d out=%q", code, out)
	}
}

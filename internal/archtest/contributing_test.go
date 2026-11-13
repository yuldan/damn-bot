package archtest

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var makeCommand = regexp.MustCompile(`(?m)^\s*(?:exec\s+)?(make(?:\s+[a-z][a-z0-9-]*)+)\s*$`)

// TestHookMatchesCI (GAP-INF-05): the local hook and CI run the same single
// entry point, CONTRIBUTING stays within 10 KiB, and the dependency policy
// paragraph exists.
func TestHookMatchesCI(t *testing.T) {
	t.Parallel()
	hook, err := os.ReadFile(repoDir() + "/.githooks/pre-commit")
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	commands := makeCommand.FindAllStringSubmatch(string(hook), -1)
	if len(commands) == 0 {
		t.Fatal("pre-commit hook runs no make command")
	}
	ci := strings.Builder{}
	for _, wf := range workflowFiles(t) {
		ci.WriteString(wf.body)
		ci.WriteByte('\n')
	}
	for _, m := range commands {
		if !strings.Contains(ci.String(), m[1]) {
			t.Errorf("hook runs %q but no CI workflow has that step", m[1])
		}
	}
	mk, err := os.ReadFile(repoDir() + "/Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if !strings.Contains(string(mk), "\ncheck:") {
		t.Error("Makefile has no check target")
	}
	info, err := os.Stat(repoDir() + "/CONTRIBUTING.md")
	if err != nil {
		t.Fatalf("stat CONTRIBUTING.md: %v", err)
	}
	if info.Size() > 10_240 {
		t.Errorf("CONTRIBUTING.md = %d bytes, limit 10240", info.Size())
	}
	eng, err := os.ReadFile(repoDir() + "/docs/ENGINEERING.md")
	if err != nil {
		t.Fatalf("read ENGINEERING: %v", err)
	}
	if !strings.Contains(string(eng), "### 4.4 Kebijakan dependency") {
		t.Error("ENGINEERING §4.4 dependency policy is missing")
	}
}

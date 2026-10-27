package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

const bundledSkillName = "code-review"

// TestBundledSkillSyncedAtStartup is the SKILL-016 binary proof: the first
// run copies the embedded corpus into <config>/skills and indexes it, the
// second run neither duplicates it nor rewrites the file, and `skills list`
// shows exactly one entry both times.
func TestBundledSkillSyncedAtStartup(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("first"), providertest.Text("second"))
	e.Run("first turn").RequireOK(t)

	configDir := spillConfigDir(e.home)
	list := e.Exec("skills", "list", "--config-dir", configDir, "--data-dir", e.DataDir)
	list.RequireOK(t)
	line, count := bundledLine(list.Stdout)
	if count != 1 {
		t.Fatalf("first skills list has %d %q entries, want 1:\n%s", count, bundledSkillName, list.Stdout)
	}
	if !strings.Contains(line, "Review a change") {
		t.Fatalf("bundled description missing from %q", line)
	}

	path := filepath.Join(configDir, "skills", bundledSkillName, "SKILL.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("bundled file: %v", err)
	}
	before := info.ModTime()

	e.Run("second turn").RequireOK(t)
	again := e.Exec("skills", "list", "--config-dir", configDir, "--data-dir", e.DataDir)
	again.RequireOK(t)
	line2, count2 := bundledLine(again.Stdout)
	if count2 != 1 {
		t.Fatalf("second skills list has %d %q entries, want 1:\n%s", count2, bundledSkillName, again.Stdout)
	}
	if line2 != line {
		t.Fatalf("bundled entry changed between runs:\nfirst:  %q\nsecond: %q", line, line2)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("bundled file after second run: %v", err)
	}
	if !after.ModTime().Equal(before) {
		t.Fatalf("second run rewrote the bundled file: %s -> %s", before, after.ModTime())
	}
}

// bundledLine returns the first `skills list` line for the bundled skill and
// how many such lines the output contains.
func bundledLine(out string) (string, int) {
	first := ""
	count := 0
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, bundledSkillName+"\t") {
			if count == 0 {
				first = line
			}
			count++
		}
	}
	return first, count
}

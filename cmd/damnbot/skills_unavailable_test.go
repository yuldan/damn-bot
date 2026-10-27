package main

import (
	"bytes"
	"strings"
	"testing"
)

// skillsReq runs one `skills` invocation and returns its streams.
func skillsReq(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append([]string{"skills"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// TestSkillsListDefaultHidesUnavailable: a skill whose host requirement is not
// met leaves the normal catalog (GAP-TOOL-10).
func TestSkillsListDefaultHidesUnavailable(t *testing.T) {
	cfg, data := t.TempDir(), t.TempDir()
	writeSkill(t, cfg, "good", "", "Review the diff.", nil)
	writeSkill(t, cfg, "wrongos", "requires: {os: [plan9]}\n", "Plan9 only.", nil)
	code, out, errOut := skillsReq(t, "list", "--config-dir", cfg, "--data-dir", data)
	if code != 0 {
		t.Fatalf("skills list = %d (%s)", code, errOut)
	}
	if !strings.Contains(out, "good\t") {
		t.Errorf("catalog lacks the available skill:\n%s", out)
	}
	if strings.Contains(out, "wrongos") {
		t.Errorf("catalog lists an unavailable skill:\n%s", out)
	}
}

// TestSkillsListUnavailableFlag: --unavailable lists exactly the rejected
// skills with the unmet requirement(s), one line each.
func TestSkillsListUnavailableFlag(t *testing.T) {
	cfg, data := t.TempDir(), t.TempDir()
	writeSkill(t, cfg, "good", "", "Review the diff.", nil)
	writeSkill(t, cfg, "wrongos", "requires: {os: [plan9], arch: [mips64]}\n", "Plan9 only.", nil)
	writeSkill(t, cfg, "nobin", "requires: {bins: [definitely-not-a-binary-f3]}\n", "Needs a tool.", nil)
	code, out, errOut := skillsReq(t, "list", "--unavailable", "--config-dir", cfg, "--data-dir", data)
	if code != 0 {
		t.Fatalf("skills list --unavailable = %d (%s)", code, errOut)
	}
	if strings.Contains(out, "good") {
		t.Errorf("--unavailable lists an available skill:\n%s", out)
	}
	for _, want := range []string{"wrongos\t", "nobin\t", "os: ", "arch: ", "bins: definitely-not-a-binary-f3 not found"} {
		if !strings.Contains(out, want) {
			t.Errorf("--unavailable lacks %q:\n%s", want, out)
		}
	}
}

// TestSkillsListUnavailableEmpty: no unavailable skill is an empty, successful
// list (never an error).
func TestSkillsListUnavailableEmpty(t *testing.T) {
	cfg, data := t.TempDir(), t.TempDir()
	writeSkill(t, cfg, "good", "", "Review the diff.", nil)
	code, out, errOut := skillsReq(t, "list", "--unavailable", "--config-dir", cfg, "--data-dir", data)
	if code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("skills list --unavailable = (%d, %q, %q)", code, out, errOut)
	}
}

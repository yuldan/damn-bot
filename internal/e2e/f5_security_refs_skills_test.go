package e2e

import "testing"

// TestF5ContextTaintBounds: prompt-borne file/URL content is untrusted input.
// The taint forces approval, the control proves the same mode reaches the
// shell without it, and an external URL is not fetched with --offline.
func TestF5ContextTaintBounds(t *testing.T) {
	t.Run("workspace file taints the turn", TestContextRefTaintsShell)
	t.Run("negative control: untainted turn runs", TestContextRefControlShellRuns)
	t.Run("external URL is not fetched offline", TestContextRefOfflineURL)
}

// TestF5SkillQuarantineBounds: a skill from a new untrusted location is
// quarantined (not offered), skill_manage still needs an approver, and a
// quarantined skill is invisible to skill_view.
func TestF5SkillQuarantineBounds(t *testing.T) {
	t.Run("new location is quarantined", TestImportedSkillQuarantined)
	t.Run("write still asks for approval", TestSkillManageAlwaysAsks)
	t.Run("quarantined skill is invisible", TestSkillViewQuarantined)
}

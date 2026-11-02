//go:build !minimal

package doctor

import (
	"strings"
	"testing"
)

func TestCredentialPoolCheck(t *testing.T) {
	t.Parallel()
	checks := PoolChecks([]CredentialPoolStatus{
		{Provider: "kantor", IDs: []string{"work", "home"}, Missing: []string{"home"}},
		{Provider: "lokal", IDs: []string{"only"}},
	})
	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1", len(checks))
	}
	got := checks[0]
	if got.ID != "DOC-012" || got.Status != StatusWarn ||
		!strings.Contains(got.Message, "kantor: 2 credential(s) [work, home]") ||
		!strings.Contains(got.Message, "missing keychain entries for [home]") {
		t.Fatalf("check = %+v", got)
	}
	if strings.Contains(got.Message, "sk-") {
		t.Fatalf("leaked value: %s", got.Message)
	}
	if ok := PoolChecks(nil); len(ok) != 1 || ok[0].Status != StatusOK || ok[0].Message != "none configured" {
		t.Fatalf("empty pools = %+v", ok)
	}
}

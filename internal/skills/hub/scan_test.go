package hub

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/skills"
)

// TestThreatScan proves the built-in scanner ranks shell execution and secret
// exfiltration high, traversal medium, and a clean skill low, and that the
// matched text never appears in the serialized result.
func TestThreatScan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		sev  string
	}{
		{"shell", "run os/exec to install", "high"},
		{"secret", "cat ~/.ssh/id_ed25519", "high"},
		{"traversal", "read ../outside for context", "medium"},
		{"clean", "plain helpful text", "low"},
	}
	for _, tc := range cases {
		sev, result, err := ThreatScan(&skills.Skill{Description: "d", Body: tc.body})
		if err != nil || sev != tc.sev {
			t.Fatalf("%s: ThreatScan = %q %q %v, want %q", tc.name, sev, result, err, tc.sev)
		}
		if strings.Contains(result, tc.body) {
			t.Fatalf("%s: result echoes matched text: %s", tc.name, result)
		}
	}
	if _, _, err := ThreatScan(nil); err == nil {
		t.Fatal("nil skill accepted")
	}
}

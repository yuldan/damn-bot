package gateway_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// nightlyNoAccount is the only account status the F7 nightly smoke matrix may
// claim while no test account exists.
const nightlyNoAccount = "no test account"

// nightlyAdapter is one row of the nightly smoke matrix: the closed SEC-10
// adapter surface and the real account it can be smoked against.
type nightlyAdapter struct {
	platform string
	account  string
}

// nightlySmokeMatrix is the recorded 30 adapter × account status table. Every
// row is `no test account`; a real account must replace its row and bring a
// nightly job with credentials before the aggregate smoke row can be LULUS.
func nightlySmokeMatrix() []nightlyAdapter {
	return []nightlyAdapter{
		{"a2a", nightlyNoAccount},
		{"api_server", nightlyNoAccount},
		{"bluebubbles", nightlyNoAccount},
		{"buzz", nightlyNoAccount},
		{"dingtalk", nightlyNoAccount},
		{"discord", nightlyNoAccount},
		{"email", nightlyNoAccount},
		{"feishu", nightlyNoAccount},
		{"googlechat", nightlyNoAccount},
		{"irc", nightlyNoAccount},
		{"line", nightlyNoAccount},
		{"matrix", nightlyNoAccount},
		{"mattermost", nightlyNoAccount},
		{"msgraph_webhook", nightlyNoAccount},
		{"ntfy", nightlyNoAccount},
		{"photon", nightlyNoAccount},
		{"qqbot", nightlyNoAccount},
		{"raft", nightlyNoAccount},
		{"signal", nightlyNoAccount},
		{"simplex", nightlyNoAccount},
		{"slack", nightlyNoAccount},
		{"sms", nightlyNoAccount},
		{"teams", nightlyNoAccount},
		{"telegram", nightlyNoAccount},
		{"webhook", nightlyNoAccount},
		{"wecom", nightlyNoAccount},
		{"weixin", nightlyNoAccount},
		{"whatsapp_cloud", nightlyNoAccount},
		{"whatsapp_web", nightlyNoAccount},
		{"yuanbao", nightlyNoAccount},
	}
}

// TestF7NightlySmokeMatrix is the machine evidence behind the aggregate
// `Smoke nyata nightly` N/A: the matrix names all 30 adapter surfaces with
// `no test account`, and the nightly workflow carries no credential source.
// A real account or a nightly secret turns this test red until the ledger row
// is upgraded.
func TestF7NightlySmokeMatrix(t *testing.T) {
	matrix := nightlySmokeMatrix()
	names := gateway.PlatformNames()
	if len(names) != 30 || len(matrix) != 30 {
		t.Fatalf("adapter surfaces = %d, matrix rows = %d, want 30 × 30", len(names), len(matrix))
	}
	for i, row := range matrix {
		if row.platform != names[i] {
			t.Errorf("row %d = %q, want %q", i, row.platform, names[i])
		}
		if row.account != nightlyNoAccount {
			t.Errorf("%s account = %q, want %q", row.platform, row.account, nightlyNoAccount)
		}
	}
	wf := repoFile(t, ".github/workflows/nightly.yml")
	if !bytes.Contains(wf, []byte("name: nightly")) {
		t.Fatal("nightly.yml is not the nightly workflow")
	}
	for _, forbidden := range []string{"secrets.", "vars.", "DAMNBOT_SMOKE"} {
		if bytes.Contains(wf, []byte(forbidden)) {
			t.Errorf("nightly.yml references %q; a nightly credential contradicts the no-account matrix", forbidden)
		}
	}
}

// repoFile reads rel from the repository root above the package directory.
func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, rel)); err == nil { //nolint:gosec // fixed repository-relative path
			return b
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("%s not found above %s", rel, dir)
		}
		dir = parent
	}
}

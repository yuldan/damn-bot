package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestDamnbotMaintenanceGateCLI (SEC15-2/11, aturan owner; e2e biner asli):
// lewat proses `damnbot` sungguhan, perintah perawatan yang destruktif
// ditolak di `--unsafe-auto-shell` — termasuk bentuk glob yang dulu lolos
// sebagai `mutating`. Kontrol positif membuktikan harness benar-benar
// menjalankan perintah yang diizinkan.
func TestDamnbotMaintenanceGateCLI(t *testing.T) {
	t.Parallel()
	bin := "'" + binPaths.main + "'"
	if runtime.GOOS == "windows" {
		// Double quotes: PowerShell runs a quoted string as a command, and the
		// cmd lexer (used before SEC10-29 lands) strips them too, so the gate
		// sees the damnbot name in both dialects. `& '...'` is only valid for
		// the PowerShell dialect and stays Mutating under the cmd lexer.
		bin = `"` + binPaths.main + `"`
	}
	cases := []struct {
		name    string
		command string
		denied  bool
	}{
		{"positive-control", "echo ok", false},
		{"purge", bin + " sessions purge s --yes", true},
		{"group-behind-glob", bin + " sess* purge --yes", true},
		{"group-glob", bin + " [s]essions purge --yes", true},
		{"unknown-global-flag", bin + " --log-level error sessions purge --yes", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := New(t,
				ToolCallReply("c1", "shell", `{"command":`+strconv.Quote(tc.command)+`}`),
				providertest.Text("done"),
			)
			// A decoy file named `sessions` makes `sess*` and `[s]essions`
			// expand to the real maintenance group at execution time.
			if err := os.WriteFile(filepath.Join(e.Workdir, "sessions"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			// SEC (ADR-069 amendment): `--unsafe-auto-shell` is dead in an
			// untrusted workspace, so the gate under test is only reachable
			// after the workspace is registered. This test is about the
			// maintenance classifier, not the trust decision (covered by
			// TestE2EUntrustedWorkspaceShellGate).
			trustWorkspace(t, e)
			res := e.Run("maintenance gate", "--approval", "auto", "--unsafe-auto-shell")
			res.RequireOK(t)
			if got := len(e.Requests()); got != 2 {
				t.Fatalf("provider calls = %d, want 2", got)
			}
			tool := e.RequestText(t, 1)
			if got := strings.Contains(tool, "denied"); got != tc.denied {
				t.Fatalf("tool result denied = %v, want %v: %s", got, tc.denied, tool)
			}
			if !tc.denied && !strings.Contains(tool, "ok") {
				t.Fatalf("positive control did not run: %s", tool)
			}
		})
	}
}

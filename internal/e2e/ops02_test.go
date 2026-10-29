package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestE2EBackgroundOffBlocksUpdate (GAP-OPS-02): with [network]
// background=false the update check is refused before a request, and the
// error names egress_disabled. The same binary reaches the server when the
// switch is on (TestE2EUpdateCheck is the positive control).
func TestE2EBackgroundOffBlocksUpdate(t *testing.T) {
	t.Parallel()
	u := newUpdateEnv(t)
	cfg := "[network]\nbackground = false\n[update]\nfeed_url = \"" + u.srv.URL + "/releases\"\n"
	if err := os.WriteFile(u.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	res := u.run(t)
	if res.Code != 1 || !strings.Contains(res.Stderr, "egress_disabled") {
		t.Fatalf("exit = %d\nstderr: %s", res.Code, res.Stderr)
	}
	if hits := u.hits.Load(); hits != 0 {
		t.Fatalf("update server received %d requests with background=false", hits)
	}

	// The env form is the same policy: an empty file plus the variable.
	if err := os.WriteFile(u.cfg, []byte("[update]\nfeed_url = \""+u.srv.URL+"/releases\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u.hits.Store(0)
	res = u.env.ExecEnv(append(u.values, "DAMNBOT_NETWORK_BACKGROUND=0"), "update", "--check", "--config", u.cfg)
	if res.Code != 1 || !strings.Contains(res.Stderr, "egress_disabled") || u.hits.Load() != 0 {
		t.Fatalf("env form: exit %d, hits %d\nstderr: %s", res.Code, u.hits.Load(), res.Stderr)
	}
}

// TestE2EBackgroundOffKeepsInference is the over-blocking control: the privacy
// switch never refuses the configured provider.
func TestE2EBackgroundOffKeepsInference(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("ok"))
	if err := os.WriteFile(e.cfg, []byte("[network]\nbackground = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.Run("hello")
	res.RequireOK(t)
	if len(e.Requests()) == 0 {
		t.Fatal("provider received no request with background=false")
	}
}

// TestE2EDoctorNetwork: `doctor --network` lists every registered egress point
// with the status the effective policy gives it (GAP-OPS-02).
func TestE2EDoctorNetwork(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeProfileConfig(t, e, "[network]\nbackground = false\n")
	res := e.Exec("doctor", "--network")
	res.RequireOK(t)
	for _, want := range []string{"inference", "refs", "telegram", "update", "gateway", "api.telegram.org", "disabled (egress_disabled)", "trigger:"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("doctor --network lacks %q:\n%s", want, res.Stdout)
		}
	}
	// Only the background entry is disabled; inference and tool stay enabled.
	if got := strings.Count(res.Stdout, "disabled (egress_disabled)"); got != 1 {
		t.Errorf("%d background entries disabled, want 1:\n%s", got, res.Stdout)
	}

	writeProfileConfig(t, e, "")
	res = e.Exec("doctor", "--network")
	res.RequireOK(t)
	if strings.Contains(res.Stdout, "egress_disabled") || !strings.Contains(res.Stdout, "background=true") {
		t.Errorf("default policy output:\n%s", res.Stdout)
	}

	js := e.Exec("doctor", "--network", "--json")
	js.RequireOK(t)
	checkDoctorNetworkJSON(t, js.Stdout, true)
	writeProfileConfig(t, e, "[network]\nbackground = false\n")
	js = e.Exec("doctor", "--network", "--json")
	js.RequireOK(t)
	checkDoctorNetworkJSON(t, js.Stdout, false)
}

// checkDoctorNetworkJSON asserts the four registered egress rows and the
// status background=true|false gives each one.
func checkDoctorNetworkJSON(t *testing.T, out string, background bool) {
	t.Helper()
	var doc struct {
		Background bool `json:"background"`
		Egress     []struct {
			Name    string `json:"name"`
			Purpose string `json:"purpose"`
			Status  string `json:"status"`
		} `json:"egress"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if doc.Background != background || len(doc.Egress) != 4 {
		t.Fatalf("json doc = %+v, want background=%t and 4 entries", doc, background)
	}
	want := map[string]struct{ purpose, status string }{
		"inference": {"inference", "enabled"},
		"refs":      {"tool", "enabled"},
		"telegram":  {"gateway", "enabled"},
		"update":    {"background", "enabled"},
	}
	if !background {
		want["update"] = struct{ purpose, status string }{"background", "disabled (egress_disabled)"}
	}
	for _, row := range doc.Egress {
		exp, ok := want[row.Name]
		if !ok || row.Purpose != exp.purpose || row.Status != exp.status {
			t.Errorf("egress %q = {%s %s}, want %+v", row.Name, row.Purpose, row.Status, exp)
		}
	}
}

// TestE2EConfigBackgroundEnv: `config explain` reports the env override and a
// malformed value is a clear, named error (GAP-OPS-02).
func TestE2EConfigBackgroundEnv(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeProfileConfig(t, e, "[network]\nbackground = true\n")
	res := e.ExecEnv([]string{"DAMNBOT_NETWORK_BACKGROUND=0"}, "config", "explain", "network.background")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "network.background = false") || !strings.Contains(res.Stdout, "source = env DAMNBOT_NETWORK_BACKGROUND") {
		t.Errorf("explain output:\n%s", res.Stdout)
	}

	bad := e.ExecEnv([]string{"DAMNBOT_NETWORK_BACKGROUND=maybe"}, "config", "explain", "network.background")
	if bad.Code == 0 || !strings.Contains(bad.Stderr, "DAMNBOT_NETWORK_BACKGROUND") {
		t.Fatalf("invalid env: exit %d\nstderr: %s", bad.Code, bad.Stderr)
	}
}

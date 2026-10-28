package e2e

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

type updateEnv struct {
	srv    *httptest.Server
	env    *Env
	cfg    string
	values []string
	tamper atomic.Bool
	hits   atomic.Int64
}

func newUpdateEnv(t *testing.T) *updateEnv {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema":       1,
		"channel":      "stable",
		"version":      "2.0.0",
		"commit":       strings.Repeat("1", 40),
		"published_at": "2026-10-27T00:00:00Z",
		"changelog":    "- perbaikan e2e",
		"assets": []map[string]string{
			{"os": "linux", "arch": "amd64", "name": "damnbot_2.0.0_linux_amd64", "sha256": strings.Repeat("a", 64)},
			{"os": "linux", "arch": "arm64", "name": "damnbot_2.0.0_linux_arm64", "sha256": strings.Repeat("b", 64)},
			{"os": "darwin", "arch": "amd64", "name": "damnbot_2.0.0_darwin_amd64", "sha256": strings.Repeat("c", 64)},
			{"os": "darwin", "arch": "arm64", "name": "damnbot_2.0.0_darwin_arm64", "sha256": strings.Repeat("d", 64)},
			{"os": "windows", "arch": "amd64", "name": "damnbot_2.0.0_windows_amd64.exe", "sha256": strings.Repeat("e", 64)},
			{"os": "windows", "arch": "arm64", "name": "damnbot_2.0.0_windows_arm64.exe", "sha256": strings.Repeat("f", 64)},
		},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sig := "damnbot-signature/v1 ed25519\n" + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)) + "\n"
	u := &updateEnv{}
	mux := http.NewServeMux()
	u.srv = httptest.NewServer(mux)
	t.Cleanup(u.srv.Close)
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) {
		u.hits.Add(1)
		if err := json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": "v2.0.0",
			"assets": []map[string]string{
				{"name": "release.json", "browser_download_url": u.srv.URL + "/release.json"},
				{"name": "release.json.sig", "browser_download_url": u.srv.URL + "/release.json.sig"},
			},
		}}); err != nil {
			t.Errorf("encode releases: %v", err)
		}
	})
	mux.HandleFunc("/release.json", func(w http.ResponseWriter, _ *http.Request) {
		if u.tamper.Load() {
			body = []byte(strings.Replace(string(body), "perbaikan e2e", "diubah", 1))
		}
		if _, err := w.Write(body); err != nil {
			t.Errorf("write manifest: %v", err)
		}
	})
	mux.HandleFunc("/release.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(sig)); err != nil {
			t.Errorf("write signature: %v", err)
		}
	})
	e := newEnv(t)
	u.env = e
	u.cfg = e.cfg
	if err := os.WriteFile(e.cfg, []byte("[update]\nfeed_url = \""+u.srv.URL+"/releases\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u.values = []string{"DAMNBOT_RELEASE_PUBKEY=" + base64.StdEncoding.EncodeToString(pub)}
	return u
}

func (u *updateEnv) run(t *testing.T) Result {
	t.Helper()
	return u.env.ExecEnv(u.values, "update", "--check", "--config", u.cfg)
}

// TestE2EUpdateCheck exercises the real binary: a valid signed release reports
// exit 10 without touching the data dir, and a tampered manifest exits 1.
func TestE2EUpdateCheck(t *testing.T) {
	u := newUpdateEnv(t)
	res := u.run(t)
	if res.Code != 10 || !strings.Contains(res.Stdout, "target 2.0.0") || !strings.Contains(res.Stdout, "perbaikan e2e") {
		t.Fatalf("valid check: exit %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	entries, err := os.ReadDir(u.env.DataDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("--check wrote into the data dir: %v, %v", entries, err)
	}
	u.tamper.Store(true)
	res = u.run(t)
	if res.Code != 1 || !strings.Contains(res.Stderr, "signature") {
		t.Fatalf("tampered manifest: exit %d\nstderr: %s", res.Code, res.Stderr)
	}
}

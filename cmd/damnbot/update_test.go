package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type assetFixture struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type manifestFixture struct {
	Schema      int            `json:"schema"`
	Channel     string         `json:"channel"`
	Version     string         `json:"version"`
	Commit      string         `json:"commit"`
	PublishedAt string         `json:"published_at"`
	Changelog   string         `json:"changelog"`
	Assets      []assetFixture `json:"assets"`
}

func updateFixture(t testing.TB) (cfgPath string, pub ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	man := manifestFixture{
		Schema:      1,
		Channel:     "stable",
		Version:     "2.0.0",
		Commit:      strings.Repeat("1", 40),
		PublishedAt: "2026-10-27T00:00:00Z",
		Changelog:   "- perbaikan uji",
		Assets: []assetFixture{
			{OS: "linux", Arch: "amd64", Name: "damnbot_2.0.0_linux_amd64", SHA256: strings.Repeat("a", 64)},
			{OS: "linux", Arch: "arm64", Name: "damnbot_2.0.0_linux_arm64", SHA256: strings.Repeat("b", 64)},
			{OS: "darwin", Arch: "amd64", Name: "damnbot_2.0.0_darwin_amd64", SHA256: strings.Repeat("c", 64)},
			{OS: "darwin", Arch: "arm64", Name: "damnbot_2.0.0_darwin_arm64", SHA256: strings.Repeat("d", 64)},
			{OS: "windows", Arch: "amd64", Name: "damnbot_2.0.0_windows_amd64.exe", SHA256: strings.Repeat("e", 64)},
			{OS: "windows", Arch: "arm64", Name: "damnbot_2.0.0_windows_arm64.exe", SHA256: strings.Repeat("f", 64)},
		},
	}
	body, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	sig := "damnbot-signature/v1 ed25519\n" + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)) + "\n"
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": "v2.0.0",
			"assets": []map[string]string{
				{"name": "release.json", "browser_download_url": srv.URL + "/release.json"},
				{"name": "release.json.sig", "browser_download_url": srv.URL + "/release.json.sig"},
			},
		}}); err != nil {
			t.Errorf("encode releases: %v", err)
		}
	})
	mux.HandleFunc("/release.json", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(body); err != nil {
			t.Errorf("write manifest: %v", err)
		}
	})
	mux.HandleFunc("/release.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, sig); err != nil {
			t.Errorf("write signature: %v", err)
		}
	})
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[update]\nfeed_url = \""+srv.URL+"/releases\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, pub
}

func TestRunUpdateCheck(t *testing.T) {
	cfg, pub := updateFixture(t)
	t.Setenv("DAMNBOT_RELEASE_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	var out bytes.Buffer
	code := runUpdate([]string{"--check", "--config", cfg}, &out, &out)
	if code != updateAvailableExit || !strings.Contains(out.String(), "target 2.0.0") {
		t.Fatalf("check exit %d: %s", code, out.String())
	}
}

func TestRunUpdateFailsClosed(t *testing.T) {
	cfg, pub := updateFixture(t)
	t.Setenv("DAMNBOT_RELEASE_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	cases := []struct {
		name string
		args []string
		env  string
		want int
	}{
		{"data without rollback", []string{"--data"}, "", 2},
		{"bad channel", []string{"--check", "--channel", "staging", "--config", cfg}, "", 2},
		{"missing key", []string{"--check", "--config", cfg}, "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "missing key" {
				t.Setenv("DAMNBOT_RELEASE_PUBKEY", "")
			}
			var out bytes.Buffer
			if code := runUpdate(tc.args, &out, &out); code != tc.want {
				t.Fatalf("exit %d, want %d: %s", code, tc.want, out.String())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg), "damnbot.new")); !os.IsNotExist(err) {
		t.Fatalf("check/failed update wrote an install artifact: %v", err)
	}
}

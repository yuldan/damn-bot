//go:build darwin

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLaunchdRoundTrip is the real-launchd contract on the macOS CI runner (no
// skip): install bootstraps a per-profile LaunchAgent, status reads it back,
// and uninstall retires it without touching the data dir. The program is
// /usr/bin/true, so the job exits immediately and KeepAlive leaves it alone.
func TestLaunchdRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dataDir, "keep.db")
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &serviceDeps{
		goos:      "darwin",
		exe:       "/usr/bin/true",
		profile:   fmt.Sprintf("svc%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000),
		home:      home,
		uid:       os.Getuid(),
		dataDir:   dataDir,
		launchctl: launchctlRun,
		stat:      os.Stat,
		readFile:  os.ReadFile,
		mkdirAll:  os.MkdirAll,
		writeFile: os.WriteFile,
		remove:    os.Remove,
	}
	label := launchdLabel(d.profile)
	plist := filepath.Join(launchdPlistDir(d), label+".plist")
	t.Cleanup(func() {
		var out, errb bytes.Buffer
		launchdUninstallCmd(context.Background(), d, nil, &out, &errb)
	})
	var stdout, stderr bytes.Buffer
	o := launchdInstallOptions{bind: "127.0.0.1:17878", dataDir: dataDir}
	if code := launchdInstall(context.Background(), d, o, &stdout, &stderr); code != 0 {
		t.Fatalf("install: code=%d stderr=%q", code, stderr.String())
	}
	raw, err := os.ReadFile(plist)
	if err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	for _, want := range []string{
		"<string>" + label + "</string>",
		"<string>/usr/bin/true</string>",
		"<string>serve</string>",
		"<string>" + dataDir + "</string>",
		"<key>DAMNBOT_SUPERVISOR</key><string>launchd</string>",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("plist missing %q:\n%s", want, raw)
		}
	}
	if strings.Contains(strings.ToLower(string(raw)), "token") {
		t.Errorf("plist carries a secret:\n%s", raw)
	}
	if st, qerr := launchdPrint(context.Background(), d, label); qerr != nil || !st.loaded {
		t.Fatalf("launchctl print after install: loaded=%v err=%v", st.loaded, qerr)
	}
	stdout.Reset()
	if code := launchdStatusCmd(context.Background(), d, []string{"--data-dir", dataDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("status: code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"label " + label, "plist yes", "loaded yes", "supervised "} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("status missing %q:\n%s", want, stdout.String())
		}
	}
	stdout.Reset()
	if code := launchdUninstallCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("uninstall: code=%d stderr=%q", code, stderr.String())
	}
	if _, serr := os.Stat(plist); !os.IsNotExist(serr) {
		t.Fatalf("plist still present: %v", serr)
	}
	if st, qerr := launchdPrint(context.Background(), d, label); qerr == nil {
		t.Fatalf("job still loaded after uninstall (state=%q)", st.state)
	}
	if _, serr := os.Stat(sentinel); serr != nil {
		t.Fatalf("uninstall removed data: %v", serr)
	}
}

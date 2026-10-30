package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLaunchd emulates the launchd job table behind the injected runner.
type fakeLaunchd struct {
	calls   []string
	loaded  bool
	print   string
	query   error
	fail    map[string]bool
	outputs map[string]string
}

func (f *fakeLaunchd) run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if f.fail[key] {
		return f.outputs[key], errors.New("launchctl: " + key)
	}
	switch args[0] {
	case "print":
		if f.query != nil {
			return "", f.query
		}
		if !f.loaded {
			return "Could not find service \"" + args[1] + "\" in domain for user gui", errors.New("exit 113")
		}
		return f.print, nil
	case "bootstrap":
		f.loaded = true
	case "bootout":
		f.loaded = false
	}
	return f.outputs[key], nil
}

func (f *fakeLaunchd) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func launchdTestDeps(t *testing.T) (*serviceDeps, *fakeLaunchd, *int) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fakeLaunchd{fail: map[string]bool{}, outputs: map[string]string{}}
	writes := 0
	return &serviceDeps{
		goos:      "darwin",
		exe:       "/Users/alice/.local/bin/damnbot",
		profile:   "",
		home:      home,
		uid:       501,
		dataDir:   filepath.Join(dir, "data"),
		launchctl: f.run,
		stat:      os.Stat,
		readFile:  os.ReadFile,
		mkdirAll:  os.MkdirAll,
		writeFile: func(p string, b []byte, mode fs.FileMode) error {
			writes++
			return os.WriteFile(p, b, mode)
		},
		remove: os.Remove,
	}, f, &writes
}

func TestRenderLaunchdPlistByteExact(t *testing.T) {
	got := renderPlist(&plistSpec{
		Label: "dev.damnbot.serve", Exe: "/Users/alice/.local/bin/damnbot", Bind: "127.0.0.1:7879",
		DataDir: "/Users/alice/Library/Application Support/damnbot",
		OutPath: "/Users/alice/Library/Logs/damnbot/launchd.out.log",
		ErrPath: "/Users/alice/Library/Logs/damnbot/launchd.err.log",
	})
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.damnbot.serve</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/alice/.local/bin/damnbot</string>
    <string>serve</string>
    <string>--bind</string>
    <string>127.0.0.1:7879</string>
    <string>--data-dir</string>
    <string>/Users/alice/Library/Application Support/damnbot</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>DAMNBOT_SUPERVISOR</key><string>launchd</string>
    <key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key><false/>
  </dict>
  <key>ThrottleInterval</key><integer>1</integer>
  <key>ExitTimeOut</key><integer>45</integer>
  <key>ProcessType</key><string>Standard</string>
  <key>SoftResourceLimits</key>
  <dict>
    <key>NumberOfFiles</key><integer>8192</integer>
  </dict>
  <key>StandardOutPath</key><string>/Users/alice/Library/Logs/damnbot/launchd.out.log</string>
  <key>StandardErrorPath</key><string>/Users/alice/Library/Logs/damnbot/launchd.err.log</string>
</dict>
</plist>
`
	if got != want {
		t.Fatalf("plist mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if strings.Contains(strings.ToLower(got), "token") || strings.Contains(strings.ToLower(got), "secret") {
		t.Error("plist must never carry a secret")
	}
}

func TestRenderLaunchdPlistEscapes(t *testing.T) {
	got := renderPlist(&plistSpec{
		Label: "dev.damnbot.serve.p1", Exe: `/opt/a & b/damn"bot`, Bind: "127.0.0.1:7879",
		DataDir: `/Users/a/<data>`, OutPath: "/L/out.log", ErrPath: "/L/err.log",
	})
	for _, want := range []string{
		`<string>/opt/a &amp; b/damn&quot;bot</string>`,
		`<string>/Users/a/&lt;data&gt;</string>`,
		`<string>dev.damnbot.serve.p1</string>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "<key>ProgramArguments</key>") != 1 {
		t.Fatalf("ProgramArguments must appear once:\n%s", got)
	}
}

func TestLaunchdInstallIdempotent(t *testing.T) {
	d, f, writes := launchdTestDeps(t)
	var stdout, stderr bytes.Buffer
	o := launchdInstallOptions{bind: "127.0.0.1:7879"}
	if code := launchdInstall(context.Background(), d, o, &stdout, &stderr); code != 0 {
		t.Fatalf("install: code=%d stderr=%q", code, stderr.String())
	}
	if *writes != 1 || f.count("bootstrap") != 1 {
		t.Fatalf("writes=%d bootstrap=%d calls=%v", *writes, f.count("bootstrap"), f.calls)
	}
	path := filepath.Join(launchdPlistDir(d), "dev.damnbot.serve.plist")
	plist, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plist), "<string>serve</string>") || !strings.Contains(string(plist), "<string>--bind</string>") {
		t.Fatalf("plist content:\n%s", plist)
	}
	stdout.Reset()
	if code := launchdInstall(context.Background(), d, o, &stdout, &stderr); code != 0 {
		t.Fatalf("second install: code=%d stderr=%q", code, stderr.String())
	}
	if *writes != 1 || f.count("bootstrap") != 1 || f.count("bootout") != 0 {
		t.Fatalf("second install had effects: writes=%d calls=%v", *writes, f.calls)
	}
	if !strings.Contains(stdout.String(), "up to date") {
		t.Fatalf("second install output = %q", stdout.String())
	}
}

func TestLaunchdInstallReloadsChangedPlist(t *testing.T) {
	d, f, writes := launchdTestDeps(t)
	var stdout, stderr bytes.Buffer
	if code := launchdInstall(context.Background(), d, launchdInstallOptions{bind: "127.0.0.1:7879"}, &stdout, &stderr); code != 0 {
		t.Fatalf("install: %s", stderr.String())
	}
	stdout.Reset()
	if code := launchdInstall(context.Background(), d, launchdInstallOptions{bind: "127.0.0.1:7880"}, &stdout, &stderr); code != 0 {
		t.Fatalf("reinstall: code=%d stderr=%q", code, stderr.String())
	}
	if *writes != 2 || f.count("bootout") != 1 || f.count("bootstrap") != 2 {
		t.Fatalf("changed plist not reloaded: writes=%d calls=%v", *writes, f.calls)
	}
}

func TestLaunchdInstallGuards(t *testing.T) {
	d, f, writes := launchdTestDeps(t)
	var stdout, stderr bytes.Buffer
	if code := launchdInstall(context.Background(), d, launchdInstallOptions{bind: "0.0.0.0:7879"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "obs.token") {
		t.Fatalf("non-loopback: code=%d stderr=%q", code, stderr.String())
	}
	if code := launchdInstall(context.Background(), d, launchdInstallOptions{bind: "127.0.0.1:7879", dryRun: true}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "<plist version=\"1.0\">") {
		t.Fatalf("dry-run: code=%d stdout=%q", code, stdout.String())
	}
	if *writes != 0 || len(f.calls) != 0 {
		t.Fatalf("guard/dry-run had effects: writes=%d calls=%v", *writes, f.calls)
	}
	d.goos = "linux"
	if code := launchdInstall(context.Background(), d, launchdInstallOptions{bind: "127.0.0.1:7879"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "macOS-only") {
		t.Fatalf("non-darwin: code=%d stderr=%q", code, stderr.String())
	}
	d.goos = "darwin"
	f.query = errors.New("launchctl missing")
	stdout.Reset()
	if code := launchdInstall(context.Background(), d, launchdInstallOptions{bind: "127.0.0.1:7879"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "not available") {
		t.Fatalf("broken launchd: code=%d stderr=%q", code, stderr.String())
	}
	if *writes != 0 {
		t.Fatalf("plist written without launchd")
	}
}

func TestLaunchdUninstallKeepsData(t *testing.T) {
	d, f, _ := launchdTestDeps(t)
	var stdout, stderr bytes.Buffer
	if code := launchdInstall(context.Background(), d, launchdInstallOptions{bind: "127.0.0.1:7879"}, &stdout, &stderr); code != 0 {
		t.Fatalf("install: %s", stderr.String())
	}
	if err := os.MkdirAll(d.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(d.dataDir, "keep.db")
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := launchdUninstallCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("uninstall: code=%d stderr=%q", code, stderr.String())
	}
	path := filepath.Join(launchdPlistDir(d), "dev.damnbot.serve.plist")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plist still present: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("uninstall removed data: %v", err)
	}
	if f.count("bootout") != 1 || !strings.Contains(stdout.String(), "data directories were not touched") {
		t.Fatalf("uninstall calls=%v stdout=%q", f.calls, stdout.String())
	}
	stdout.Reset()
	if code := launchdUninstallCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "not present") {
		t.Fatalf("second uninstall: code=%d stdout=%q", code, stdout.String())
	}
}

func TestParseLaunchdPrint(t *testing.T) {
	out := "gui/501/dev.damnbot.serve = {\n" +
		"\tactive count = 1\n" +
		"\tpath = /Users/alice/Library/LaunchAgents/dev.damnbot.serve.plist\n" +
		"\tstate = running\n" +
		"\tprogram = /Users/alice/.local/bin/damnbot\n" +
		"\targuments = {\n" +
		"\t\tstate = nested-must-not-win\n" +
		"\t\tpid = 999\n" +
		"\t}\n" +
		"\tpid = 4242\n" +
		"\tlast exit status = {\n" +
		"\t\tcode = 3\n" +
		"\t}\n" +
		"\tlast exit code = 78\n" +
		"}\n"
	st := parseLaunchdPrint(out)
	if st.state != "running" || st.pid != 4242 {
		t.Fatalf("parsed state=%q pid=%d", st.state, st.pid)
	}
	if st.lastExit == nil || *st.lastExit != 78 {
		t.Fatalf("last exit = %v, want 78", st.lastExit)
	}
	if st := parseLaunchdPrint("\tstate = waiting\n"); st.state != "waiting" || st.pid != 0 || st.lastExit != nil {
		t.Fatalf("minimal parse = %+v", st)
	}
}

func TestLaunchdStatusClassification(t *testing.T) {
	d, f, _ := launchdTestDeps(t)
	var stdout, stderr bytes.Buffer
	if err := os.MkdirAll(launchdPlistDir(d), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launchdPlistDir(d), "dev.damnbot.serve.plist"), []byte("x"), 0o644); err != nil { // #nosec G306 -- fixture
		t.Fatal(err)
	}
	f.loaded = true
	f.print = "gui/501/dev.damnbot.serve = {\n\tstate = running\n\tpid = 4242\n\tlast exit code = 0\n}\n"
	if code := launchdStatusCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("status: code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"label dev.damnbot.serve", "plist yes", "loaded yes", "state running", "pid 4242", "last exit 0", "supervised launchd (pid 4242)"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("status missing %q:\n%s", want, stdout.String())
		}
	}

	// Lease outside launchd = interactive.
	f.loaded = false
	if err := os.MkdirAll(filepath.Join(d.dataDir, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.dataDir, "run", "serve.lock"), []byte("\x00pid 777 lang=go"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := launchdStatusCmd(context.Background(), d, []string{"--data-dir", d.dataDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("status: code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "supervised interactive (pid 777)") || !strings.Contains(stdout.String(), "loaded no") {
		t.Errorf("interactive classification:\n%s", stdout.String())
	}

	// A failing probe is unknown and nonzero, never a confident claim.
	f.query = errors.New("launchctl unavailable")
	stdout.Reset()
	if code := launchdStatusCmd(context.Background(), d, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("broken probe code = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "supervised unknown") {
		t.Errorf("unknown classification:\n%s", stdout.String())
	}
}

func TestLaunchdLogsTail(t *testing.T) {
	d, _, _ := launchdTestDeps(t)
	if err := os.MkdirAll(launchdLogsDir(d), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := range 150 {
		b.WriteString("line")
		b.WriteString(strings.Repeat("x", i%3))
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(launchdLogsDir(d), "launchd.err.log"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := launchdLogsCmd(d, []string{"logs"}, &stdout, &stderr); code != 0 {
		t.Fatalf("logs: code=%d stderr=%q", code, stderr.String())
	}
	if got := strings.Count(stdout.String(), "\n"); got != 3+launchdLogTail {
		t.Fatalf("log lines = %d, want %d\n%s", got, 3+launchdLogTail, stdout.String())
	}
	if !strings.Contains(stdout.String(), "== ") || !strings.Contains(stdout.String(), "(no log)") {
		t.Fatalf("logs output:\n%s", stdout.String())
	}
}

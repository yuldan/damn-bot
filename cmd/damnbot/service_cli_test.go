package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/heal"
)

// fakeSystemctl records every call and answers from outputs.
type fakeSystemctl struct {
	calls   []string
	outputs map[string]string
	fail    map[string]bool
}

func (f *fakeSystemctl) run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if f.fail[key] {
		return f.outputs[key], errFakeSystemctl
	}
	return f.outputs[key], nil
}

type fakeSystemctlError struct{}

func (fakeSystemctlError) Error() string { return "systemctl unavailable" }

var errFakeSystemctl = fakeSystemctlError{}

// serviceTestDeps builds isolated deps over t.TempDir and a fake systemctl.
func serviceTestDeps(t *testing.T) (*serviceDeps, *fakeSystemctl, *int) {
	t.Helper()
	dir := t.TempDir()
	sys := &fakeSystemctl{outputs: map[string]string{}, fail: map[string]bool{}}
	writes := 0
	return &serviceDeps{
		goos:     "linux",
		exe:      "/home/alice/.local/bin/damnbot",
		unitDir:  filepath.Join(dir, "systemd", "user"),
		dataDir:  filepath.Join(dir, "data"),
		sysctl:   sys.run,
		stat:     os.Stat,
		readFile: os.ReadFile,
		mkdirAll: os.MkdirAll,
		writeFile: func(p string, b []byte, mode fs.FileMode) error {
			writes++
			return os.WriteFile(p, b, mode) // #nosec G306 -- unit files are world-readable
		},
		remove: os.Remove,
	}, sys, &writes
}

func TestRenderUnitByteExact(t *testing.T) {
	got := renderUnit(unitSpec{Exe: "/home/alice/.local/bin/damnbot", Bind: "127.0.0.1:7879"})
	want := `[Unit]
Description=Damn-Bot daemon (damnbot serve)
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=exec
ExecStart=/home/alice/.local/bin/damnbot serve --bind 127.0.0.1:7879
EnvironmentFile=-%h/.config/damnbot/telegram.env
Environment=DAMNBOT_SUPERVISOR=systemd
Restart=on-failure
RestartSec=1
RestartPreventExitStatus=78
TimeoutStartSec=30
TimeoutStopSec=45
KillMode=mixed
KillSignal=SIGTERM
LimitNOFILE=8192
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`
	if got != want {
		t.Fatalf("unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if strings.Contains(strings.ToLower(got), "token") {
		t.Error("unit must never carry a secret")
	}
}

func TestRenderUnitEscapesPaths(t *testing.T) {
	got := renderUnit(unitSpec{
		Exe:     `/opt/my bot/damn%bot$`,
		Bind:    "127.0.0.1:7879",
		DataDir: `/home/a "quoted"/data`,
	})
	wantLine := `ExecStart="/opt/my bot/damn%%bot$$" serve --bind 127.0.0.1:7879 --data-dir "/home/a \"quoted\"/data"`
	if !strings.Contains(got, wantLine) {
		t.Fatalf("ExecStart escaping:\n%s\nwant line: %s", got, wantLine)
	}
	if strings.Count(got, "ExecStart=") != 1 {
		t.Fatalf("ExecStart must appear once:\n%s", got)
	}
}

func TestInstallIdempotent(t *testing.T) {
	d, sys, writes := serviceTestDeps(t)
	var stdout, stderr bytes.Buffer
	o := serviceInstallOptions{bind: "127.0.0.1:7879"}
	if code := serviceInstall(context.Background(), d, o, &stdout, &stderr); code != 0 {
		t.Fatalf("first install: code=%d stderr=%q", code, stderr.String())
	}
	if *writes != 1 {
		t.Fatalf("writes = %d, want 1", *writes)
	}
	stdout.Reset()
	if code := serviceInstall(context.Background(), d, o, &stdout, &stderr); code != 0 {
		t.Fatalf("second install: code=%d stderr=%q", code, stderr.String())
	}
	if *writes != 1 {
		t.Fatalf("second install rewrote the unit (writes=%d)", *writes)
	}
	if !strings.Contains(stdout.String(), "up to date") {
		t.Fatalf("second install output = %q", stdout.String())
	}
	if slices.Contains(sys.calls, "enable --now damnbot.service") {
		t.Fatalf("install without --enable started the unit: %v", sys.calls)
	}
	if !slices.Contains(sys.calls, "daemon-reload") {
		t.Fatalf("daemon-reload missing: %v", sys.calls)
	}
	unit, err := os.ReadFile(filepath.Join(d.unitDir, serviceUnitName)) // #nosec G304 -- test path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "ExecStart=/home/alice/.local/bin/damnbot serve --bind 127.0.0.1:7879") {
		t.Fatalf("unit content:\n%s", unit)
	}
}

func TestInstallEnableStartsUnit(t *testing.T) {
	d, sys, _ := serviceTestDeps(t)
	var stdout, stderr bytes.Buffer
	o := serviceInstallOptions{bind: "127.0.0.1:7879", enable: true}
	if code := serviceInstall(context.Background(), d, o, &stdout, &stderr); code != 0 {
		t.Fatalf("install: code=%d stderr=%q", code, stderr.String())
	}
	if !slices.Contains(sys.calls, "enable --now damnbot.service") {
		t.Fatalf("enable not called: %v", sys.calls)
	}
}

func TestInstallRejectsNonLoopback(t *testing.T) {
	d, sys, writes := serviceTestDeps(t)
	var stdout, stderr bytes.Buffer
	code := serviceInstall(context.Background(), d, serviceInstallOptions{bind: "0.0.0.0:7879"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "obs.token") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if *writes != 0 || len(sys.calls) != 0 {
		t.Fatalf("refused install touched the system: writes=%d calls=%v", *writes, sys.calls)
	}
}

func TestInstallRequiresSystemd(t *testing.T) {
	d, sys, writes := serviceTestDeps(t)
	sys.fail["show-environment"] = true
	var stdout, stderr bytes.Buffer
	code := serviceInstall(context.Background(), d, serviceInstallOptions{bind: "127.0.0.1:7879"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "not available") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if *writes != 0 {
		t.Fatalf("unit written without a systemd user manager")
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	d, sys, writes := serviceTestDeps(t)
	var stdout, stderr bytes.Buffer
	if code := serviceInstall(context.Background(), d, serviceInstallOptions{bind: "127.0.0.1:7879", dryRun: true}, &stdout, &stderr); code != 0 {
		t.Fatalf("dry-run: code=%d stderr=%q", code, stderr.String())
	}
	if *writes != 0 || len(sys.calls) != 0 {
		t.Fatalf("dry-run had effects: writes=%d calls=%v", *writes, sys.calls)
	}
	if !strings.Contains(stdout.String(), "ExecStart=") {
		t.Fatalf("dry-run output = %q", stdout.String())
	}
}

func TestUninstallKeepsData(t *testing.T) {
	d, sys, _ := serviceTestDeps(t)
	var stdout, stderr bytes.Buffer
	if code := serviceInstall(context.Background(), d, serviceInstallOptions{bind: "127.0.0.1:7879"}, &stdout, &stderr); code != 0 {
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
	if code := serviceUninstallCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("uninstall: code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(d.unitDir, serviceUnitName)); !os.IsNotExist(err) {
		t.Fatalf("unit file still present: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("uninstall removed data: %v", err)
	}
	if !slices.Contains(sys.calls, "stop damnbot.service") || !slices.Contains(sys.calls, "disable damnbot.service") {
		t.Fatalf("uninstall did not stop/disable: %v", sys.calls)
	}
	if !strings.Contains(stdout.String(), "data directories were not touched") {
		t.Fatalf("uninstall output = %q", stdout.String())
	}
}

func TestStatusReadsUnitAndLedger(t *testing.T) {
	d, sys, _ := serviceTestDeps(t)
	sys.outputs["show-environment"] = ""
	sys.outputs["is-active damnbot.service"] = "active\n"
	sys.outputs["is-enabled damnbot.service"] = "enabled\n"
	sys.outputs["show -p MainPID --value damnbot.service"] = "4242\n"
	sys.outputs["show -p ActiveEnterTimestampMonotonic --value damnbot.service"] = "0\n"
	if err := os.MkdirAll(d.unitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.unitDir, serviceUnitName), []byte("x"), 0o644); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d.dataDir, "supervise"), 0o700); err != nil {
		t.Fatal(err)
	}
	ledger := `{"ts":1700000000,"boot_id":"b","pid":1,"version":"v","kind":"panic","exit":2,"subsystem":"store"}` + "\n" +
		`{"ts":1700000100,"boot_id":"b","pid":2,"version":"v","kind":"hang","exit":70}` + "\n" +
		`{"ts":1700000200,` // torn tail
	if err := os.WriteFile(filepath.Join(d.dataDir, "supervise", "boots.jsonl"), []byte(ledger), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := serviceStatusCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("status: code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"unit damnbot.service", "state active", "enabled enabled", "pid 4242",
		"mode unknown (no data dir)", "ledger 2",
		"kind=panic exit=2 subsystem=store", "kind=hang exit=70 subsystem=-",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

func TestResumeRejectsWhenNotSafeMode(t *testing.T) {
	d, _, _ := serviceTestDeps(t)
	var stdout, stderr bytes.Buffer
	if code := serviceResumeCmd(context.Background(), d, []string{"--no-restart"}, &stdout, &stderr); code == 0 {
		t.Fatalf("resume without a data dir succeeded: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "not in safe mode") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestResumeClearsPersistedBreaker(t *testing.T) {
	d, _, _ := serviceTestDeps(t)
	if err := os.MkdirAll(d.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: d.dataDir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if err = heal.NewBreaker(db, nil).Trip(context.Background(), "5 crashes"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := serviceResumeCmd(context.Background(), d, []string{"--no-restart"}, &stdout, &stderr); code != 0 {
		t.Fatalf("resume: code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "left SAFE MODE") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	db, err = ctxdb.Open(context.Background(), &ctxdb.Options{Dir: d.dataDir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Fatal(cerr)
		}
	}()
	st, err := heal.NewBreaker(db, nil).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.SafeMode {
		t.Fatalf("breaker still in safe mode: %+v", st)
	}
}

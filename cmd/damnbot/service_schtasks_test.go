package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

type fakeWinExit struct{}

func (fakeWinExit) Error() string { return "exit status 1" }

// fakeWin is the injected schtasks runner: every argv is recorded so tests
// assert the exact command, not just "some output".
type fakeWin struct {
	calls   []string
	out     map[string]string
	fail    map[string]bool
	allow   map[string]bool
	missing bool
	staged  []byte
	cleaned bool

	leasePID  int
	alive     bool
	aliveErr  error
	hosted    bool
	hostedErr error
}

func (f *fakeWin) run(_ context.Context, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if f.missing {
		return nil, errWinToolMissing
	}
	if f.fail[key] {
		return []byte(f.out[key]), fakeWinExit{}
	}
	// Absent tasks answer with exit 1, like schtasks does. A verb must be
	// allow-listed (allow[argv[0]]) to succeed, so a forgotten fixture fails.
	if _, ok := f.out[key]; ok || f.allow[args[0]] {
		return []byte(f.out[key]), nil
	}
	return nil, fakeWinExit{}
}

func newFakeWin() *fakeWin {
	return &fakeWin{out: map[string]string{}, fail: map[string]bool{}, allow: map[string]bool{}}
}

// winDriveData is an absolute Windows data dir used where the value reaches
// the task XML; file-backed assertions keep the real t.TempDir path.
const winDriveData = `C:\Users\alice\data`

// winTestDeps builds isolated deps over t.TempDir. Its GOOS is data, so the
// same core runs on the Linux runner.
func winTestDeps(t *testing.T, profile string) (*winServiceDeps, *fakeWin, string) {
	t.Helper()
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	f := newFakeWin()
	base := &serviceDeps{
		goos: "windows", exe: `C:\Program Files\Damn-Bot\damnbot.exe`,
		dataDir: data, stat: os.Stat, readFile: os.ReadFile,
		mkdirAll: os.MkdirAll, writeFile: os.WriteFile, remove: os.Remove,
	}
	d := &winServiceDeps{
		base: base, profile: profile, user: `DESKTOP-01\alice`, run: f.run,
		stage: func(b []byte) (string, func() error, error) {
			f.staged = b
			return `C:\Temp\damnbot-task.xml`, func() error { f.cleaned = true; return nil }, nil
		},
		leasePID: func(string) int { return f.leasePID },
		alive:    func(int) (bool, error) { return f.alive, f.aliveErr },
		hosted:   func(int) (bool, error) { return f.hosted, f.hostedErr },
	}
	return d, f, data
}

// winInstallArgs turns install options into the CLI arguments under test.
func winInstallArgs(o winInstallOptions) []string {
	args := []string{"--bind", o.bind}
	if o.dataDir != "" {
		args = append(args, "--data-dir", o.dataDir)
	}
	if o.enable {
		args = append(args, "--enable")
	}
	if o.dryRun {
		args = append(args, "--dry-run")
	}
	return args
}

// utf16LE encodes text as UTF-16LE with a BOM, like localized schtasks.
func utf16LE(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 2, 2+2*len(units))
	out[0], out[1] = 0xFF, 0xFE
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

func TestWinTaskNameIsProfileScoped(t *testing.T) {
	t.Parallel()
	if got := winTaskName(""); got != "Damn-Bot-default" {
		t.Fatalf("default task name = %q", got)
	}
	if got := winTaskName("work"); got != "Damn-Bot-work" {
		t.Fatalf("profile task name = %q", got)
	}
	for _, legacy := range winLegacyTaskNames() {
		if slices.Contains([]string{winTaskName(""), winTaskName("work")}, legacy) {
			t.Fatalf("new task name reuses legacy %q (EC-WIN-004)", legacy)
		}
	}
}

func TestDecodeTaskOutputUTF16LE(t *testing.T) {
	t.Parallel()
	const want = "Tugas: Berjalan — ñ 日本語 €\r\nStatus: Ready\r\n"
	got, err := decodeTaskOutput(utf16LE(want))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("decoded = %q, want %q", got, want)
	}
}

func TestDecodeTaskOutputUTF8Fallback(t *testing.T) {
	t.Parallel()
	const want = "TaskName: \\Damn-Bot-default\r\nStatus: Ready\r\n"
	got, err := decodeTaskOutput([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("decoded = %q", got)
	}
}

func TestDecodeTaskOutputRejectsBroken(t *testing.T) {
	t.Parallel()
	cases := map[string][]byte{
		"odd UTF-16LE body": {0xFF, 0xFE, 'a'},
		"UTF-16BE BOM":      {0xFE, 0xFF, 0x00, 'a'},
		"invalid UTF-8":     {0xFF, 0xFF, 0xFF},
		"unpaired high":     {0xFF, 0xFE, 0x00, 0xD8, 'a', 0x00},
		"unpaired low":      {0xFF, 0xFE, 0x00, 0xDC},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeTaskOutput(raw)
			if err == nil {
				t.Fatalf("decoded %q without an error", got)
			}
			if !errors.Is(err, errTaskOutput) {
				t.Fatalf("error %v does not wrap errTaskOutput", err)
			}
		})
	}
}

func TestRenderTaskXMLArgsAndNoSecret(t *testing.T) {
	t.Parallel()
	spec := &winTaskSpec{
		Name: "Damn-Bot-default", Exe: `C:\Program Files\Damn-Bot\damnbot.exe`,
		Bind: "127.0.0.1:7879", DataDir: `C:\Users\alice\AppData\Local\Damn-Bot`, User: `DESKTOP-01\alice`,
	}
	xml := renderTaskXML(spec)
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<Command>C:\Program Files\Damn-Bot\damnbot.exe</Command>`,
		`<Arguments>serve --bind 127.0.0.1:7879 --data-dir &quot;C:\Users\alice\AppData\Local\Damn-Bot&quot;</Arguments>`,
		`<LogonTrigger>`, `<UserId>DESKTOP-01\alice</UserId>`, `<RunLevel>LeastPrivilege</RunLevel>`,
		`<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task XML missing %q:\n%s", want, xml)
		}
	}
	for _, secret := range []string{"api_token", "password", "<Environment", "telegram.env", "<Value>"} {
		if strings.Contains(strings.ToLower(xml), strings.ToLower(secret)) {
			t.Errorf("task XML carries %q; the definition must stay secret-free", secret)
		}
	}
	if strings.Count(xml, "<Arguments>") != 1 {
		t.Fatalf("Arguments must appear once:\n%s", xml)
	}
}

func TestRenderTaskXMLUTF16RoundTrip(t *testing.T) {
	t.Parallel()
	spec := &winTaskSpec{Exe: `C:\d\a & b.exe`, Bind: "127.0.0.1:7879", User: `D\alice & bob`}
	encoded := encodeTaskXML(renderTaskXML(spec))
	if len(encoded) < 2 || encoded[0] != 0xFF || encoded[1] != 0xFE {
		t.Fatalf("task XML is not UTF-16LE with BOM: % x", encoded[:min(4, len(encoded))])
	}
	decoded, err := decodeTaskOutput(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded, `<Command>C:\d\a &amp; b.exe</Command>`) {
		t.Fatalf("XML escaping lost:\n%s", decoded)
	}
	if !strings.Contains(decoded, `<UserId>D\alice &amp; bob</UserId>`) {
		t.Fatalf("user escaping lost:\n%s", decoded)
	}
}

func TestCheckWinDataDir(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{`data\dir`, `C:relative`, `C:\dir\`, `C:\a"b`, "C:\\a\nb", `\\`} {
		if err := checkWinDataDir(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, ok := range []string{`C:\Users\alice\data`, `D:/data/damnbot`, `\\server\share\damnbot`} {
		if err := checkWinDataDir(ok); err != nil {
			t.Errorf("rejected %q: %v", ok, err)
		}
	}
}

func TestWinInstallExactArgs(t *testing.T) {
	t.Parallel()
	d, f, _ := winTestDeps(t, "")
	f.allow["/Create"] = true
	var stdout, stderr bytes.Buffer
	o := winInstallOptions{bind: "127.0.0.1:7879", dataDir: winDriveData}
	if code := winInstallCmd(context.Background(), d, winInstallArgs(o), &stdout, &stderr); code != 0 {
		t.Fatalf("install: code=%d stderr=%q", code, stderr.String())
	}
	want := []string{"/Create", "/TN", "Damn-Bot-default", "/XML", `C:\Temp\damnbot-task.xml`, "/F"}
	if got := f.calls[len(f.calls)-1]; got != strings.Join(want, " ") {
		t.Fatalf("install argv = %q, want %q\nall calls: %v", got, strings.Join(want, " "), f.calls)
	}
	for _, legacy := range winLegacyTaskNames() {
		if !slices.Contains(f.calls, "/Query /TN "+legacy) {
			t.Errorf("install did not probe legacy task %q: %v", legacy, f.calls)
		}
	}
	if !f.cleaned {
		t.Fatal("staged task XML was not removed")
	}
	decoded, err := decodeTaskOutput(f.staged)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded, `<Arguments>serve --bind 127.0.0.1:7879 --data-dir &quot;`+winDriveData+`&quot;</Arguments>`) {
		t.Fatalf("staged XML arguments:\n%s", decoded)
	}
	if slices.Contains(f.calls, "/Run /TN Damn-Bot-default") {
		t.Fatalf("install without --enable ran the task: %v", f.calls)
	}
}

func TestWinInstallIdempotent(t *testing.T) {
	t.Parallel()
	d, f, _ := winTestDeps(t, "work")
	f.allow["/Create"] = true
	var stdout, stderr bytes.Buffer
	o := winInstallOptions{bind: "127.0.0.1:7879", dataDir: winDriveData}
	for i := range 2 {
		if code := winInstallCmd(context.Background(), d, winInstallArgs(o), &stdout, &stderr); code != 0 {
			t.Fatalf("install #%d: code=%d stderr=%q", i+1, code, stderr.String())
		}
	}
	creates := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, "/Create /TN Damn-Bot-work ") {
			creates++
		}
	}
	if creates != 2 {
		t.Fatalf("creates = %d, want one idempotent /F create per install: %v", creates, f.calls)
	}
}

func TestWinInstallEnableAndRetiresLegacy(t *testing.T) {
	t.Parallel()
	d, f, _ := winTestDeps(t, "")
	f.out["/Query /TN DamnBot"] = "TaskName: \\DamnBot\r\n"
	f.allow["/Create"], f.allow["/End"], f.allow["/Delete"], f.allow["/Run"] = true, true, true, true
	var stdout, stderr bytes.Buffer
	o := winInstallOptions{bind: "127.0.0.1:7879", dataDir: winDriveData, enable: true}
	if code := winInstallCmd(context.Background(), d, winInstallArgs(o), &stdout, &stderr); code != 0 {
		t.Fatalf("install: code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"/End /TN DamnBot", "/Delete /TN DamnBot /F", "/Run /TN Damn-Bot-default"} {
		if !slices.Contains(f.calls, want) {
			t.Errorf("missing %q in %v", want, f.calls)
		}
	}
	if !strings.Contains(stdout.String(), "started task Damn-Bot-default") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWinInstallRejectsBadInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		o    winInstallOptions
	}{
		{"non-loopback bind", winInstallOptions{bind: "0.0.0.0:7879"}},
		{"relative data dir", winInstallOptions{bind: "127.0.0.1:7879", dataDir: `data\dir`}},
		{"quoted data dir", winInstallOptions{bind: "127.0.0.1:7879", dataDir: `C:\a"b`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, f, _ := winTestDeps(t, "")
			var stdout, stderr bytes.Buffer
			if code := winInstallCmd(context.Background(), d, winInstallArgs(tc.o), &stdout, &stderr); code == 0 {
				t.Fatalf("accepted %+v", tc.o)
			}
			if len(f.calls) != 0 {
				t.Fatalf("refused install touched the scheduler: %v", f.calls)
			}
		})
	}
}

func TestWinInstallDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	d, f, _ := winTestDeps(t, "")
	var stdout, stderr bytes.Buffer
	o := winInstallOptions{bind: "127.0.0.1:7879", dryRun: true}
	if code := winInstallCmd(context.Background(), d, winInstallArgs(o), &stdout, &stderr); code != 0 {
		t.Fatalf("dry-run: code=%d stderr=%q", code, stderr.String())
	}
	if len(f.calls) != 0 || f.staged != nil {
		t.Fatalf("dry-run had effects: calls=%v staged=%v", f.calls, f.staged != nil)
	}
	if !strings.Contains(stdout.String(), "<Arguments>serve --bind 127.0.0.1:7879</Arguments>") {
		t.Fatalf("dry-run output = %q", stdout.String())
	}
}

func TestWinUninstallRetiresAllNamesKeepsData(t *testing.T) {
	t.Parallel()
	d, f, data := winTestDeps(t, "work")
	names := append([]string{"Damn-Bot-work"}, winLegacyTaskNames()...)
	f.allow["/End"], f.allow["/Delete"] = true, true
	for _, name := range names {
		f.out["/Query /TN "+name] = "TaskName: \\" + name + "\r\n"
	}
	sentinel := filepath.Join(data, "keep.db")
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := winUninstallCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("uninstall: code=%d stderr=%q", code, stderr.String())
	}
	for _, name := range names {
		for _, want := range []string{"/End /TN " + name, "/Delete /TN " + name + " /F"} {
			if !slices.Contains(f.calls, want) {
				t.Errorf("missing %q in %v", want, f.calls)
			}
		}
		if !strings.Contains(stdout.String(), "removed task "+name) {
			t.Errorf("stdout does not report removal of %q: %q", name, stdout.String())
		}
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("uninstall removed data: %v", err)
	}
	if !strings.Contains(stdout.String(), "data directories were not touched") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWinUninstallIdempotent(t *testing.T) {
	t.Parallel()
	d, f, _ := winTestDeps(t, "")
	var stdout, stderr bytes.Buffer
	for i := range 2 {
		if code := winUninstallCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("uninstall #%d: code=%d stderr=%q", i+1, code, stderr.String())
		}
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "/Delete") || strings.HasPrefix(call, "/End") {
			t.Fatalf("absent tasks were deleted: %v", f.calls)
		}
	}
	if !strings.Contains(stdout.String(), "task not present: Damn-Bot-default") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWinUninstallMissingSchtasksIsAnError(t *testing.T) {
	t.Parallel()
	d, f, _ := winTestDeps(t, "")
	f.missing = true
	var stdout, stderr bytes.Buffer
	if code := winUninstallCmd(context.Background(), d, nil, &stdout, &stderr); code == 0 {
		t.Fatalf("missing schtasks reported success: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "schtasks is not available") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestWinStatusSupervision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		pid        int
		registered bool
		alive      bool
		aliveErr   error
		hosted     bool
		hostedErr  error
		wantState  string
		wantSup    string
	}{
		{"task scheduler parent", 4001, true, true, nil, true, nil, "running", "task-scheduler"},
		{"interactive process", 4002, true, true, nil, false, nil, "running", "interactive"},
		{"probe failure is unknown", 4003, true, true, nil, false, errors.New("access denied"), "running", "unknown"},
		{"stale lease is idle", 4004, true, false, nil, false, nil, "idle", "none (stale lease)"},
		{"not installed, not running", 0, false, false, nil, false, nil, "absent", "none"},
		{"registered but idle", 0, true, false, nil, false, nil, "idle", "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, f, data := winTestDeps(t, "")
			if tc.registered {
				f.out["/Query /TN Damn-Bot-default"] = "TaskName: \\Damn-Bot-default\r\n"
			}
			f.leasePID, f.alive, f.aliveErr, f.hosted, f.hostedErr = tc.pid, tc.alive, tc.aliveErr, tc.hosted, tc.hostedErr
			if err := os.MkdirAll(filepath.Join(data, "supervise"), 0o700); err != nil {
				t.Fatal(err)
			}
			ledger := `{"ts":1700000000,"boot_id":"b","pid":1,"version":"v","kind":"panic","exit":2,"subsystem":"store"}` + "\n"
			if err := os.WriteFile(filepath.Join(data, "supervise", "boots.jsonl"), []byte(ledger), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := winStatusCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 {
				t.Fatalf("status: code=%d stderr=%q", code, stderr.String())
			}
			out := stdout.String()
			for _, want := range []string{
				"task Damn-Bot-default", registeredWant(tc.registered),
				"state " + tc.wantState, "supervised " + tc.wantSup,
				"mode unknown (no data dir)", "ledger 1", "kind=panic exit=2 subsystem=store",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("status missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestWinStatusWithoutSchtasksFails(t *testing.T) {
	t.Parallel()
	d, f, _ := winTestDeps(t, "")
	f.missing = true
	var stdout, stderr bytes.Buffer
	if code := winStatusCmd(context.Background(), d, nil, &stdout, &stderr); code == 0 {
		t.Fatalf("status without schtasks succeeded: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "schtasks is not available") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestWinLogsDecodesUTF16Query(t *testing.T) {
	t.Parallel()
	d, f, data := winTestDeps(t, "")
	f.out["/Query /TN Damn-Bot-default /V /FO LIST"] = string(utf16LE("Nama Tugas: \\Damn-Bot-default\r\nStatus: Berjalan — ñ\r\n"))
	if err := os.MkdirAll(filepath.Join(data, "supervise"), 0o700); err != nil {
		t.Fatal(err)
	}
	ledger := `{"ts":1700000000,"boot_id":"b","pid":1,"version":"v","kind":"hang","exit":70,"subsystem":"loop"}` + "\n"
	if err := os.WriteFile(filepath.Join(data, "supervise", "boots.jsonl"), []byte(ledger), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := winLogsCmd(context.Background(), d, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("logs: code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Nama Tugas: \\Damn-Bot-default", "Status: Berjalan — ñ", "kind=hang exit=70"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}
}

func TestLeaseOwnerPID(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"pid 4242 started 2026-10-29T00:00:00Z": 4242,
		"pid 7 started x":                       7,
		"owner unknown":                         0,
		"pid notanumber":                        0,
		"pid -3":                                0,
		"pid 9":                                 9,
	}
	for owner, want := range cases {
		if got := leaseOwnerPID(owner); got != want {
			t.Errorf("leaseOwnerPID(%q) = %d, want %d", owner, got, want)
		}
	}
}

func registeredWant(v bool) string {
	if v {
		return "registered yes"
	}
	return "registered no"
}

func TestWinRunUnknownSubcommand(t *testing.T) {
	t.Parallel()
	d, _, _ := winTestDeps(t, "")
	var stdout, stderr bytes.Buffer
	if code := winRun(context.Background(), d, []string{"bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

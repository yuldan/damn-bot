package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/heal"
	"github.com/damnbot/damnbot/internal/obs"
)

// Linux systemd user unit (SELF-HEALING §1.5); macOS launchd and Windows
// Task Scheduler have their own backends in service_launchd.go and
// service_schtasks.go.
const (
	serviceUnitName   = "damnbot.service"
	serviceLedgerTail = 10
)

// serviceDeps routes every effect through one seam for tests.
type serviceDeps struct {
	goos      string
	exe       string
	unitDir   string
	dataDir   string
	profile   string
	home      string
	uid       int
	sysctl    func(context.Context, ...string) (string, error)
	launchctl func(context.Context, ...string) (string, error)
	stat      func(string) (fs.FileInfo, error)
	readFile  func(string) ([]byte, error)
	mkdirAll  func(string, fs.FileMode) error
	writeFile func(string, []byte, fs.FileMode) error
	remove    func(string) error
}

func serviceDepsFrom() (*serviceDeps, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		home = ""
	}
	uid := -1
	if u, uerr := user.Current(); uerr == nil {
		if n, nerr := strconv.Atoi(u.Uid); nerr == nil {
			uid = n
		}
	}
	return &serviceDeps{
		goos: runtime.GOOS, exe: exe, unitDir: filepath.Join(paths.ConfigDir, "systemd", "user"), dataDir: paths.DataDir,
		profile: activeProfile(), home: home, uid: uid,
		sysctl: systemctl, launchctl: launchctlRun, stat: os.Stat, readFile: os.ReadFile,
		mkdirAll: os.MkdirAll, writeFile: os.WriteFile, remove: os.Remove,
	}, nil
}

func systemctl(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...) // #nosec G204 -- fixed binary, literal args
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func launchctlRun(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "launchctl", args...) // #nosec G204 -- fixed binary, literal args
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func runService(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		serviceUsage(stderr)
		return 2
	}
	if runtime.GOOS == "windows" {
		return runWindowsService(args, stdout, stderr)
	}
	d, err := serviceDepsFrom()
	if err != nil {
		sessionsPrintf(stderr, "service: %s\n", cleanLine(err.Error()))
		return 1
	}
	switch {
	case d.goos == "darwin":
		return runLaunchdService(context.Background(), d, args, stdout, stderr)
	case args[0] == "install":
		return serviceInstallCmd(context.Background(), d, args[1:], stdout, stderr)
	case args[0] == "uninstall":
		return serviceUninstallCmd(context.Background(), d, args[1:], stdout, stderr)
	case args[0] == "status":
		return serviceStatusCmd(context.Background(), d, args[1:], stdout, stderr)
	case args[0] == "resume":
		return serviceResumeCmd(context.Background(), d, args[1:], stdout, stderr)
	case args[0] == "logs":
		return serviceLogsCmd(context.Background(), d, args[1:], stdout, stderr)
	}
	sessionsPrintf(stderr, "service: unknown subcommand %q\n", cleanArg(args[0]))
	serviceUsage(stderr)
	return 2
}

func serviceUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot service install [--bind ADDR] [--data-dir DIR] [--unit-dir DIR] [--enable] [--dry-run]\n")
	sessionsPrintf(w, "       damnbot service uninstall|status|resume|logs\n")
	sessionsPrintf(w, "Linux: systemd user unit. macOS: launchd LaunchAgent. Windows: Task Scheduler task per profile (schtasks).\n")
}

type unitSpec struct {
	Exe     string
	Bind    string
	DataDir string
}

// renderUnit follows §1.5 but uses Type=exec: serve sends no sd_notify yet.
func renderUnit(s unitSpec) string {
	exec := systemdArg(s.Exe) + " serve --bind " + systemdArg(s.Bind)
	if s.DataDir != "" {
		exec += " --data-dir " + systemdArg(s.DataDir)
	}
	return serviceUnitHead + exec + serviceUnitTail
}

const serviceUnitHead = `[Unit]
Description=Damn-Bot daemon (damnbot serve)
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=exec
ExecStart=`

// The unit names the operator's 0600 env file, never its contents.
const serviceUnitTail = `
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

func systemdArg(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "%", "%%"), "$", "$$")
	if !strings.ContainsAny(s, " \t\n\"'\\") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

type serviceInstallOptions struct {
	bind, dataDir, unitDir string
	enable, dryRun         bool
}

func serviceInstallCmd(ctx context.Context, d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	var o serviceInstallOptions
	if !serviceParse("service install", args, stderr, func(flags *flag.FlagSet) {
		flags.StringVar(&o.bind, "bind", obs.DefaultListen, "observability listener in ExecStart (loopback only)")
		flags.StringVar(&o.dataDir, "data-dir", "", "data directory passed to serve (default: profile data dir)")
		flags.StringVar(&o.unitDir, "unit-dir", "", "unit directory (default: <config>/systemd/user)")
		flags.BoolVar(&o.enable, "enable", false, "also run systemctl --user enable --now")
		flags.BoolVar(&o.dryRun, "dry-run", false, "print the unit; write nothing")
	}) {
		return 2
	}
	return serviceInstall(ctx, d, o, stdout, stderr)
}

func serviceInstall(ctx context.Context, d *serviceDeps, o serviceInstallOptions, stdout, stderr io.Writer) int {
	if d.goos != "linux" {
		sessionsPrintf(stderr, "service install: %s is not supported in this slice; only Linux systemd user units are implemented (launchd/Windows are deferred F4 work)\n", d.goos)
		return 1
	}
	if err := checkInstallBind(o.bind); err != nil {
		sessionsPrintf(stderr, "service install: %s\n", cleanLine(err.Error()))
		return 1
	}
	if o.dataDir != "" && (!filepath.IsAbs(o.dataDir) || strings.ContainsAny(o.dataDir, "\x00\r\n")) {
		sessionsPrintf(stderr, "service install: --data-dir must be an absolute single-line path\n")
		return 2
	}
	unit := renderUnit(unitSpec{Exe: d.exe, Bind: o.bind, DataDir: o.dataDir})
	if o.dryRun {
		sessionsPrintf(stdout, "%s", unit)
		return 0
	}
	if _, err := d.sysctl(ctx, "show-environment"); err != nil {
		sessionsPrintf(stderr, "service install: systemd user manager is not available (%s); nothing was written\n", cleanLine(err.Error()))
		return 1
	}
	dir := d.unitDir
	if o.unitDir != "" {
		dir = o.unitDir
	}
	if err := d.mkdirAll(dir, 0o755); err != nil {
		sessionsPrintf(stderr, "service install: create unit directory: %s\n", cleanLine(err.Error()))
		return 1
	}
	path := filepath.Join(dir, serviceUnitName)
	wrote, err := writeUnitFile(d, path, []byte(unit))
	if err != nil {
		sessionsPrintf(stderr, "service install: write unit: %s\n", cleanLine(err.Error()))
		return 1
	}
	if _, err := d.sysctl(ctx, "daemon-reload"); err != nil {
		sessionsPrintf(stderr, "service install: %s\n", cleanLine(err.Error()))
		return 1
	}
	if wrote {
		sessionsPrintf(stdout, "installed %s\n", path)
	} else {
		sessionsPrintf(stdout, "up to date %s\n", path)
	}
	if err := installEnable(ctx, d, o.enable, stdout); err != nil {
		sessionsPrintf(stderr, "service install: %s\n", cleanLine(err.Error()))
		return 1
	}
	return 0
}

func installEnable(ctx context.Context, d *serviceDeps, enable bool, stdout io.Writer) error {
	if !enable {
		sessionsPrintf(stdout, "unit written and reloaded; not enabled (pass --enable to enable and start)\n")
	} else if _, err := d.sysctl(ctx, "enable", "--now", serviceUnitName); err != nil {
		return fmt.Errorf("enable unit: %w", err)
	} else {
		sessionsPrintf(stdout, "enabled %s (started)\n", serviceUnitName)
	}
	return nil
}

func checkInstallBind(bind string) error {
	if err := obs.ValidateListen(bind); err != nil {
		return fmt.Errorf("%w (non-loopback needs obs.token, not implemented; use %s)", err, obs.DefaultListen)
	}
	return nil
}

func writeUnitFile(d *serviceDeps, path string, unit []byte) (bool, error) {
	old, err := d.readFile(path)
	switch {
	case err == nil && bytes.Equal(old, unit):
		return false, nil
	case err == nil, errors.Is(err, fs.ErrNotExist):
	default:
		return false, err
	}
	if err := d.writeFile(path, unit, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func serviceUninstallCmd(ctx context.Context, d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	var unitDir *string
	if !serviceParse("service uninstall", args, stderr, func(flags *flag.FlagSet) {
		unitDir = flags.String("unit-dir", "", "unit directory (default: <config>/systemd/user)")
	}) {
		return 2
	}
	if d.goos != "linux" {
		sessionsPrintf(stderr, "service uninstall: %s is not supported in this slice; only Linux systemd user units are implemented\n", d.goos)
		return 1
	}
	manager := true
	if _, err := d.sysctl(ctx, "show-environment"); err != nil {
		manager = false
	}
	if manager {
		for _, verb := range []string{"stop", "disable"} {
			if out, err := d.sysctl(ctx, verb, serviceUnitName); err != nil && !strings.Contains(strings.ToLower(out), "not ") {
				sessionsPrintf(stderr, "service uninstall: %s\n", cleanLine(err.Error()))
			}
		}
	}
	dir := d.unitDir
	if *unitDir != "" {
		dir = *unitDir
	}
	path := filepath.Join(dir, serviceUnitName)
	switch err := d.remove(path); {
	case err == nil:
		sessionsPrintf(stdout, "removed %s\n", path)
	case errors.Is(err, fs.ErrNotExist):
		sessionsPrintf(stdout, "unit file not present: %s\n", path)
	default:
		sessionsPrintf(stderr, "service uninstall: remove unit: %s\n", cleanLine(err.Error()))
		return 1
	}
	if manager {
		if _, err := d.sysctl(ctx, "daemon-reload"); err != nil {
			sessionsPrintf(stderr, "service uninstall: %s\n", cleanLine(err.Error()))
		}
	}
	sessionsPrintf(stdout, "data directories were not touched\n")
	return 0
}

func serviceStatusCmd(ctx context.Context, d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	var dataDir, unitDir *string
	if !serviceParse("service status", args, stderr, func(flags *flag.FlagSet) {
		dataDir = flags.String("data-dir", "", "data directory (default: profile data dir)")
		unitDir = flags.String("unit-dir", "", "unit directory (default: <config>/systemd/user)")
	}) {
		return 2
	}
	if d.goos != "linux" {
		sessionsPrintf(stderr, "service status: %s is not supported in this slice; only Linux systemd user units are implemented\n", d.goos)
		return 1
	}
	if _, err := d.sysctl(ctx, "show-environment"); err != nil {
		sessionsPrintf(stderr, "service status: systemd user manager is not available (%s)\n", cleanLine(err.Error()))
		return 1
	}
	dir, data := d.unitDir, d.dataDir
	if *unitDir != "" {
		dir = *unitDir
	}
	if *dataDir != "" {
		data = *dataDir
	}
	path := filepath.Join(dir, serviceUnitName)
	sessionsPrintf(stdout, "unit %s\nfile %s\nstate %s\nenabled %s\npid %s\nmode %s\n", serviceUnitName, path,
		sysctlValue(ctx, d, "is-active", serviceUnitName), sysctlValue(ctx, d, "is-enabled", serviceUnitName),
		sysctlValue(ctx, d, "show", "-p", "MainPID", "--value", serviceUnitName), serviceMode(ctx, d, data))
	ledger := serviceLedger(d, data)
	sessionsPrintf(stdout, "ledger %d\n", len(ledger))
	for _, line := range ledger {
		sessionsPrintf(stdout, "  %s\n", line)
	}
	return 0
}

func sysctlValue(ctx context.Context, d *serviceDeps, args ...string) string {
	out, _ := d.sysctl(ctx, args...) //nolint:errcheck // nonzero exit still prints the state
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	if line == "" {
		return "unknown"
	}
	return cleanLine(line)
}

func serviceMode(ctx context.Context, d *serviceDeps, dataDir string) string {
	if _, err := d.stat(filepath.Join(dataDir, "damnbot.db")); err != nil {
		return "unknown (no data dir)"
	}
	db, err := openSessionsDB(ctx, dataDir)
	if err != nil {
		return "unknown (" + cleanLine(err.Error()) + ")"
	}
	//nolint:contextcheck // ctxdb.Store.Close has no context parameter
	defer func() { _ = db.Close() }() //nolint:errcheck // status is best-effort
	st, err := heal.NewBreaker(db, nil).Load(ctx)
	switch {
	case err != nil:
		return "unknown (" + cleanLine(err.Error()) + ")"
	case st.SafeMode:
		return "safe_mode (" + cleanLine(st.Reason) + ")"
	}
	return "normal"
}

func serviceLedger(d *serviceDeps, dataDir string) []string {
	raw, err := d.readFile(filepath.Join(dataDir, "supervise", "boots.jsonl"))
	if err != nil {
		return nil
	}
	var out []string
	for line := range strings.Lines(string(raw)) {
		var e map[string]json.RawMessage
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &e) != nil {
			continue
		}
		out = append(out, fmt.Sprintf("crash %s kind=%s exit=%s subsystem=%s",
			ledgerField(e, "ts"), ledgerField(e, "kind"), ledgerField(e, "exit"), ledgerField(e, "subsystem")))
	}
	if len(out) > serviceLedgerTail {
		out = out[len(out)-serviceLedgerTail:]
	}
	return out
}

func ledgerField(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return "-"
	}
	return cleanLine(strings.Trim(string(raw), `"`))
}

func serviceResumeCmd(ctx context.Context, d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	var dataDir *string
	var noRestart *bool
	if !serviceParse("service resume", args, stderr, func(flags *flag.FlagSet) {
		dataDir = flags.String("data-dir", "", "data directory (default: profile data dir)")
		noRestart = flags.Bool("no-restart", false, "clear SAFE MODE but do not restart an active unit")
	}) {
		return 2
	}
	if d.goos != "linux" {
		sessionsPrintf(stderr, "service resume: %s is not supported in this slice; only Linux systemd user units are implemented\n", d.goos)
		return 1
	}
	data := d.dataDir
	if *dataDir != "" {
		data = *dataDir
	}
	if _, err := d.stat(filepath.Join(data, "damnbot.db")); err != nil {
		sessionsPrintf(stderr, "service resume: %s\n", heal.ErrNotSafeMode)
		return 1
	}
	db, err := openSessionsDB(ctx, data)
	if err != nil {
		sessionsPrintf(stderr, "service resume: %s\n", cleanLine(err.Error()))
		return 1
	}
	_, rerr := heal.NewBreaker(db, nil).Resume(ctx)
	//nolint:contextcheck // ctxdb.Store.Close has no context parameter
	_ = db.Close() //nolint:errcheck // resume already succeeded
	if rerr != nil {
		sessionsPrintf(stderr, "service resume: %s\n", cleanLine(rerr.Error()))
		return 1
	}
	sessionsPrintf(stdout, "resumed: left SAFE MODE; crash-loop counter reset\n")
	if !*noRestart {
		if _, err := d.sysctl(ctx, "show-environment"); err == nil && sysctlValue(ctx, d, "is-active", serviceUnitName) == "active" {
			if _, err := d.sysctl(ctx, "restart", serviceUnitName); err != nil {
				sessionsPrintf(stderr, "service resume: %s\n", cleanLine(err.Error()))
				return 1
			}
			sessionsPrintf(stdout, "restarted %s\n", serviceUnitName)
		}
	}
	return 0
}

func serviceLogsCmd(ctx context.Context, d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 || d.goos != "linux" {
		sessionsPrintf(stderr, "service logs: no arguments; Linux journal only\n")
		return 2
	}
	cmd := exec.CommandContext(ctx, "journalctl", "--user", "-u", serviceUnitName, "-n", "100", "--no-pager") // #nosec G204 -- fixed binary, literal args
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		sessionsPrintf(stderr, "service logs: %s\n", cleanLine(err.Error()))
		return 1
	}
	return 0
}

func serviceParse(name string, args []string, stderr io.Writer, defs func(*flag.FlagSet)) bool {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	defs(flags)
	if err := flags.Parse(args); err != nil {
		return false
	}
	if flags.NArg() != 0 {
		sessionsPrintf(stderr, "%s: unexpected arguments: %v\n", name, flags.Args())
		return false
	}
	return true
}

package main

// macOS launchd backend of `damnbot service` (SELF-HEALING §1.6, ADR-256):
// one LaunchAgent per profile runs `<exe> serve --bind …`; the plist, parser
// and classifier are portable, only the launchctl calls touch the OS.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/obs"
)

const (
	launchdLabelBase = "dev.damnbot.serve"
	launchdPathEnv   = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	launchdLogTail   = 100
)

// errLaunchdMissing distinguishes an absent job from a broken launchd.
var errLaunchdMissing = errors.New("launchd: service not loaded")

func launchdLabel(profile string) string {
	if profile == "" {
		return launchdLabelBase
	}
	return launchdLabelBase + "." + profile
}
func launchdDomain(uid int) string { return "gui/" + strconv.Itoa(uid) }
func launchdPlistDir(d *serviceDeps) string {
	return filepath.Join(d.home, "Library", "LaunchAgents")
}

func launchdLogsDir(d *serviceDeps) string {
	return filepath.Join(d.home, "Library", "Logs", "damnbot")
}

// launchdState is the subset of `launchctl print` status reads.
type launchdState struct {
	loaded   bool
	state    string
	pid      int
	lastExit *int
}

// launchdSupervision: launchd (label running), interactive (lease outside
// launchd), or unknown (probe failed).
func launchdSupervision(st launchdState, qerr error, leasePID int) string {
	switch {
	case errors.Is(qerr, errLaunchdMissing):
		if leasePID > 0 {
			return "interactive (pid " + strconv.Itoa(leasePID) + ")"
		}
		return "none"
	case qerr != nil:
		return "unknown (" + cleanLine(qerr.Error()) + ")"
	case st.pid > 0:
		return "launchd (pid " + strconv.Itoa(st.pid) + ")"
	case leasePID > 0:
		return "interactive (pid " + strconv.Itoa(leasePID) + ")"
	default:
		return "none"
	}
}

// parseLaunchdPrint reads only top-level `key = value` lines, so nested dicts
// cannot shadow state/pid/last exit.
func parseLaunchdPrint(out string) launchdState {
	var st launchdState
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimLeft(line, " \t")
		switch lead := line[:len(line)-len(trimmed)]; lead {
		case "":
			continue
		case "\t", "  ":
		default:
			continue
		}
		key, value, ok := strings.Cut(trimmed, " = ")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "state":
			st.state = strings.TrimSpace(value)
		case "pid":
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n > 0 {
				st.pid = n
			}
		case "last exit code", "last exit status":
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				st.lastExit = &n
			}
		}
	}
	return st
}

func launchdPrint(ctx context.Context, d *serviceDeps, label string) (launchdState, error) {
	out, err := d.launchctl(ctx, "print", launchdDomain(d.uid)+"/"+label)
	if err != nil {
		lower := strings.ToLower(out)
		if strings.Contains(lower, "could not find service") || strings.Contains(lower, "no such process") || strings.Contains(lower, "not found") {
			return launchdState{}, errLaunchdMissing
		}
		return launchdState{}, err
	}
	st := parseLaunchdPrint(out)
	st.loaded = true
	return st, nil
}

type launchdInstallOptions struct {
	bind, dataDir string
	dryRun        bool
}

type plistSpec struct {
	Label, Exe, Bind, DataDir, OutPath, ErrPath string
}

// renderPlist is the SELF-HEALING §1.6 LaunchAgent byte for byte; its
// environment carries only the supervisor marker and PATH.
func renderPlist(s *plistSpec) string {
	data := ""
	if s.DataDir != "" {
		data = "\n    <string>--data-dir</string>\n    <string>" + xmlText(s.DataDir) + "</string>"
	}
	return fmt.Sprintf(launchdPlistBody, xmlText(s.Label), xmlText(s.Exe), xmlText(s.Bind), data,
		xmlText(s.OutPath), xmlText(s.ErrPath))
}

const launchdPlistBody = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>serve</string>
    <string>--bind</string>
    <string>%s</string>%s
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>DAMNBOT_SUPERVISOR</key><string>launchd</string>
    <key>PATH</key><string>` + launchdPathEnv + `</string>
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
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`

func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

func runLaunchdService(ctx context.Context, d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		serviceUsage(stderr)
		return 2
	}
	switch args[0] {
	case "install":
		return launchdInstallCmd(ctx, d, args[1:], stdout, stderr)
	case "uninstall":
		return launchdUninstallCmd(ctx, d, args[1:], stdout, stderr)
	case "status":
		return launchdStatusCmd(ctx, d, args[1:], stdout, stderr)
	case "logs":
		return launchdLogsCmd(d, args, stdout, stderr)
	case "resume":
		sessionsPrintf(stderr, "service resume: launchd restart is not implemented yet; use `launchctl kickstart -k %s/%s` after `damnbot service status`\n",
			launchdDomain(d.uid), launchdLabel(d.profile))
		return 1
	}
	sessionsPrintf(stderr, "service: unknown subcommand %q\n", cleanArg(args[0]))
	serviceUsage(stderr)
	return 2
}

func launchdInstallCmd(ctx context.Context, d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	var o launchdInstallOptions
	if !serviceParse("service install", args, stderr, func(flags *flag.FlagSet) {
		flags.StringVar(&o.bind, "bind", obs.DefaultListen, "observability listener (loopback only)")
		flags.StringVar(&o.dataDir, "data-dir", "", "data directory passed to serve (default: profile data dir)")
		flags.BoolVar(&o.dryRun, "dry-run", false, "print the plist; write nothing")
	}) {
		return 2
	}
	return launchdInstall(ctx, d, o, stdout, stderr)
}

func launchdValidate(d *serviceDeps, o launchdInstallOptions, stderr io.Writer) int {
	if d.goos != "darwin" {
		sessionsPrintf(stderr, "service install: launchd is macOS-only (this is %s)\n", d.goos)
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
	return 0
}

// launchdPlan returns the label, plist path, log directory and plist bytes.
func launchdPlan(d *serviceDeps, o launchdInstallOptions) (label, path, logs, plist string) {
	label = launchdLabel(d.profile)
	logs = launchdLogsDir(d)
	plist = renderPlist(&plistSpec{
		Label: label, Exe: d.exe, Bind: o.bind, DataDir: o.dataDir,
		OutPath: filepath.Join(logs, "launchd.out.log"), ErrPath: filepath.Join(logs, "launchd.err.log"),
	})
	return label, filepath.Join(launchdPlistDir(d), label+".plist"), logs, plist
}

func launchdWrite(d *serviceDeps, path, logs, plist string, stderr io.Writer) (bool, int) {
	if err := d.mkdirAll(filepath.Dir(path), 0o755); err != nil {
		sessionsPrintf(stderr, "service install: create LaunchAgents directory: %s\n", cleanLine(err.Error()))
		return false, 1
	}
	if err := d.mkdirAll(logs, 0o700); err != nil {
		sessionsPrintf(stderr, "service install: create log directory: %s\n", cleanLine(err.Error()))
		return false, 1
	}
	wrote, err := writeUnitFile(d, path, []byte(plist))
	if err != nil {
		sessionsPrintf(stderr, "service install: write plist: %s\n", cleanLine(err.Error()))
		return false, 1
	}
	return wrote, 0
}

func launchdActivate(ctx context.Context, d *serviceDeps, label, path string, wrote, loaded bool, stdout, stderr io.Writer) int {
	if !wrote && loaded {
		sessionsPrintf(stdout, "up to date %s\n", path)
		return 0
	}
	if loaded {
		if _, err := d.launchctl(ctx, "bootout", launchdDomain(d.uid)+"/"+label); err != nil {
			sessionsPrintf(stderr, "service install: reload warning (bootout): %s\n", cleanLine(err.Error()))
		}
	}
	if _, err := d.launchctl(ctx, "bootstrap", launchdDomain(d.uid), path); err != nil {
		sessionsPrintf(stderr, "service install: %s\n", cleanLine(err.Error()))
		return 1
	}
	if wrote {
		sessionsPrintf(stdout, "installed %s\n", path)
	} else {
		sessionsPrintf(stdout, "reloaded %s\n", path)
	}
	sessionsPrintf(stdout, "loaded %s (runs at login; no secret is stored in the plist)\n", label)
	return 0
}

func launchdInstall(ctx context.Context, d *serviceDeps, o launchdInstallOptions, stdout, stderr io.Writer) int {
	if code := launchdValidate(d, o, stderr); code != 0 {
		return code
	}
	label, path, logs, plist := launchdPlan(d, o)
	if o.dryRun {
		sessionsPrintf(stdout, "%s", plist)
		return 0
	}
	st, qerr := launchdPrint(ctx, d, label)
	if qerr != nil && !errors.Is(qerr, errLaunchdMissing) {
		sessionsPrintf(stderr, "service install: launchd is not available (%s); nothing was written\n", cleanLine(qerr.Error()))
		return 1
	}
	wrote, code := launchdWrite(d, path, logs, plist, stderr)
	if code != 0 {
		return code
	}
	return launchdActivate(ctx, d, label, path, wrote, qerr == nil && st.loaded, stdout, stderr)
}

func launchdUninstallCmd(ctx context.Context, d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	if !serviceParse("service uninstall", args, stderr, func(*flag.FlagSet) {}) {
		return 2
	}
	if d.goos != "darwin" {
		sessionsPrintf(stderr, "service uninstall: launchd is macOS-only (this is %s)\n", d.goos)
		return 1
	}
	label := launchdLabel(d.profile)
	if out, err := d.launchctl(ctx, "bootout", launchdDomain(d.uid)+"/"+label); err != nil {
		lower := strings.ToLower(out)
		if !strings.Contains(lower, "could not find service") && !strings.Contains(lower, "no such process") {
			sessionsPrintf(stderr, "service uninstall: %s\n", cleanLine(err.Error()))
		}
	}
	path := filepath.Join(launchdPlistDir(d), label+".plist")
	switch err := d.remove(path); {
	case err == nil:
		sessionsPrintf(stdout, "removed %s\n", path)
	case errors.Is(err, fs.ErrNotExist):
		sessionsPrintf(stdout, "plist not present: %s\n", path)
	default:
		sessionsPrintf(stderr, "service uninstall: remove plist: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "data directories were not touched\n")
	return 0
}

func launchdStatusCmd(ctx context.Context, d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	var dataDir *string
	if !serviceParse("service status", args, stderr, func(flags *flag.FlagSet) {
		dataDir = flags.String("data-dir", "", "data directory (default: profile data dir)")
	}) {
		return 2
	}
	if d.goos != "darwin" {
		sessionsPrintf(stderr, "service status: launchd is macOS-only (this is %s)\n", d.goos)
		return 1
	}
	data := d.dataDir
	if *dataDir != "" {
		data = *dataDir
	}
	label := launchdLabel(d.profile)
	path := filepath.Join(launchdPlistDir(d), label+".plist")
	st, qerr := launchdPrint(ctx, d, label)
	loaded, state, pid, lastExit := "no", "not loaded", "-", "-"
	if qerr == nil {
		loaded = "yes"
		state = st.state
		if state == "" {
			state = "unknown"
		}
		if st.pid > 0 {
			pid = strconv.Itoa(st.pid)
		}
		if st.lastExit != nil {
			lastExit = strconv.Itoa(*st.lastExit)
		}
	}
	plist := "no"
	if _, statErr := d.stat(path); statErr == nil {
		plist = "yes"
	}
	lease := leaseOwnerPID(lockOwner(filepath.Join(data, "run", "serve.lock")))
	sessionsPrintf(stdout, "label %s\nfile %s\nplist %s\nloaded %s\nstate %s\npid %s\nlast exit %s\nsupervised %s\nmode %s\n",
		label, path, plist, loaded, state, pid, lastExit, launchdSupervision(st, qerr, lease), serviceMode(ctx, d, data))
	ledger := serviceLedger(d, data)
	sessionsPrintf(stdout, "ledger %d\n", len(ledger))
	for _, line := range ledger {
		sessionsPrintf(stdout, "  %s\n", line)
	}
	if qerr != nil && !errors.Is(qerr, errLaunchdMissing) {
		sessionsPrintf(stderr, "service status: %s\n", cleanLine(qerr.Error()))
		return 1
	}
	return 0
}

func launchdLogsCmd(d *serviceDeps, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || d.goos != "darwin" {
		sessionsPrintf(stderr, "service logs: no arguments; macOS launchd logs\n")
		return 2
	}
	for _, name := range []string{"out", "err"} {
		path := filepath.Join(launchdLogsDir(d), "launchd."+name+".log")
		raw, err := d.readFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			sessionsPrintf(stdout, "== %s ==\n(no log)\n", path)
			continue
		case err != nil:
			sessionsPrintf(stderr, "service logs: %s: %s\n", path, cleanLine(err.Error()))
			return 1
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		if len(lines) > launchdLogTail {
			lines = lines[len(lines)-launchdLogTail:]
		}
		sessionsPrintf(stdout, "== %s ==\n%s\n", path, strings.Join(lines, "\n"))
	}
	return 0
}

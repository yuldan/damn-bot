package main

// Windows Task Scheduler backend of `damnbot service` (SELF-HEALING §1.7,
// ADR-251): a per-profile LogonTrigger task runs `<exe> serve --bind …`. The
// logic is portable so the UTF-16 decoder (EC-WIN-006), task XML, exact
// schtasks argv, legacy-name retirement (EC-WIN-004) and supervision
// classification (EC-WIN-003) are unit-tested on Linux.

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/obs"
)

const winTaskPrefix = "Damn-Bot"

var (
	// errTaskOutput marks output that is neither UTF-16LE+BOM nor UTF-8.
	errTaskOutput = errors.New("task output encoding")
	// errWinToolMissing means schtasks cannot run at all, unlike a task that
	// is merely absent (a normal non-zero exit).
	errWinToolMissing = errors.New("schtasks is not available")
)

// winLegacyTaskNames are the profile-less names older installs used; two
// profiles shared one task and the duplicate gateway ran twice (EC-WIN-004).
func winLegacyTaskNames() []string { return []string{"Damn-Bot", "DamnBot", "damnbot"} }

// winTaskName always suffixes the profile: never a shared task name.
func winTaskName(profile string) string {
	if profile == "" {
		return winTaskPrefix + "-default"
	}
	return winTaskPrefix + "-" + profile
}

// decodeTaskOutput decodes schtasks console output: UTF-16LE with a BOM on
// localized Windows, UTF-8 as-is, and a hard error otherwise (EC-WIN-006).
func decodeTaskOutput(raw []byte) (string, error) {
	switch {
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE:
		return decodeUTF16LE(raw[2:])
	case len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF:
		return "", fmt.Errorf("%w: UTF-16BE BOM, schtasks writes UTF-16LE", errTaskOutput)
	case utf8.Valid(raw):
		return strings.TrimPrefix(string(raw), "\ufeff"), nil
	default:
		return "", fmt.Errorf("%w: no UTF-16LE BOM and the bytes are not valid UTF-8", errTaskOutput)
	}
}

func decodeUTF16LE(body []byte) (string, error) {
	if len(body)%2 != 0 {
		return "", fmt.Errorf("%w: UTF-16LE body has odd length %d", errTaskOutput, len(body))
	}
	units := make([]uint16, 0, len(body)/2)
	for i := 0; i < len(body); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(body[i:i+2]))
	}
	if len(units) > 0 && units[0] == 0xFEFF {
		units = units[1:]
	}
	for i := 0; i < len(units); i++ {
		switch u := units[i]; {
		case 0xD800 <= u && u <= 0xDBFF:
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				return "", fmt.Errorf("%w: unpaired UTF-16 high surrogate at unit %d", errTaskOutput, i)
			}
			i++
		case 0xDC00 <= u && u <= 0xDFFF:
			return "", fmt.Errorf("%w: unpaired UTF-16 low surrogate at unit %d", errTaskOutput, i)
		}
	}
	return string(utf16.Decode(units)), nil
}

type winRunner func(ctx context.Context, args ...string) ([]byte, error)

// winServiceDeps is one Windows invocation: paths/files from base plus the OS
// probes that status reports (EC-WIN-003).
type winServiceDeps struct {
	base     *serviceDeps
	profile  string
	user     string
	run      winRunner
	stage    func([]byte) (string, func() error, error)
	leasePID func(dataDir string) int
	alive    func(pid int) (bool, error)
	hosted   func(pid int) (bool, error)
}

func winRun(ctx context.Context, d *winServiceDeps, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		serviceUsage(stderr)
		return 2
	}
	switch args[0] {
	case "install":
		return winInstallCmd(ctx, d, args[1:], stdout, stderr)
	case "uninstall":
		return winUninstallCmd(ctx, d, args[1:], stdout, stderr)
	case "status":
		return winStatusCmd(ctx, d, args[1:], stdout, stderr)
	case "logs":
		return winLogsCmd(ctx, d, args[1:], stdout, stderr)
	case "resume":
		sessionsPrintf(stderr, "service resume: Windows Task Scheduler restart is not implemented yet (F4); use status/logs\n")
		return 1
	}
	sessionsPrintf(stderr, "service: unknown subcommand %q\n", cleanArg(args[0]))
	serviceUsage(stderr)
	return 2
}

// winSchtasks runs one schtasks argv and decodes its output strictly; a
// non-zero exit still carries the decoded scheduler message.
func winSchtasks(ctx context.Context, d *winServiceDeps, args ...string) (string, error) {
	raw, runErr := d.run(ctx, args...)
	text, decErr := decodeTaskOutput(raw)
	call := "schtasks " + strings.Join(args, " ")
	if runErr != nil {
		if decErr != nil {
			return "", fmt.Errorf("%s: %w (output undecodable: %w)", call, runErr, decErr)
		}
		return text, fmt.Errorf("%s: %w: %s", call, runErr, cleanLine(text))
	}
	if decErr != nil {
		return "", fmt.Errorf("%s: %w", call, decErr)
	}
	return text, nil
}

// winTaskSpec is everything the registered task contains: no Environment
// element exists, so no secret can enter it.
type winTaskSpec struct {
	Name, Exe, Bind, DataDir, User string
}

func renderTaskXML(s *winTaskSpec) string {
	args := "serve --bind " + s.Bind
	if s.DataDir != "" {
		args += " --data-dir " + `"` + s.DataDir + `"`
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	b.WriteString(`<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	b.WriteString("  <RegistrationInfo>\n    <Description>Damn-Bot daemon (damnbot serve)</Description>\n  </RegistrationInfo>\n")
	fmt.Fprintf(&b, "  <Triggers>\n    <LogonTrigger>\n      <Enabled>true</Enabled>\n      <UserId>%s</UserId>\n    </LogonTrigger>\n  </Triggers>\n", xmlEscape(s.User))
	fmt.Fprintf(&b, "  <Principals>\n    <Principal id=\"Author\">\n      <UserId>%s</UserId>\n      <LogonType>InteractiveToken</LogonType>\n      <RunLevel>LeastPrivilege</RunLevel>\n    </Principal>\n  </Principals>\n", xmlEscape(s.User))
	b.WriteString("  <Settings>\n" +
		"    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\n" +
		"    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\n" +
		"    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\n" +
		"    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>\n" +
		"    <RestartOnFailure>\n      <Interval>PT1M</Interval>\n      <Count>999</Count>\n    </RestartOnFailure>\n" +
		"    <StartWhenAvailable>true</StartWhenAvailable>\n" +
		"    <Hidden>true</Hidden>\n" +
		"  </Settings>\n")
	fmt.Fprintf(&b, "  <Actions Context=\"Author\">\n    <Exec>\n      <Command>%s</Command>\n      <Arguments>%s</Arguments>\n    </Exec>\n  </Actions>\n</Task>\n",
		xmlEscape(s.Exe), xmlEscape(args))
	return b.String()
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// encodeTaskXML writes UTF-16LE with a BOM: schtasks rejects UTF-8 XML from
// non-English Windows consoles (EC-WIN-006).
func encodeTaskXML(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 2+2*len(units))
	out[0], out[1] = 0xFF, 0xFE
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[2+2*i:], u)
	}
	return out
}

// checkWinDataDir applies Windows path rules on every host.
func checkWinDataDir(p string) error {
	abs := strings.HasPrefix(p, `\\`) ||
		len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
			('A' <= p[0] && p[0] <= 'Z' || 'a' <= p[0] && p[0] <= 'z')
	switch {
	case !abs:
		return errors.New("--data-dir must be an absolute Windows path (C:\\… or \\\\server\\share)")
	case strings.ContainsAny(p, "\x00\r\n\"") || strings.HasSuffix(p, `\`):
		return errors.New("--data-dir must not contain a quote, control character, or trailing backslash")
	}
	return nil
}

type winInstallOptions struct {
	bind, dataDir  string
	enable, dryRun bool
}

func winInstallCmd(ctx context.Context, d *winServiceDeps, args []string, stdout, stderr io.Writer) int {
	var o winInstallOptions
	if !serviceParse("service install", args, stderr, func(flags *flag.FlagSet) {
		flags.StringVar(&o.bind, "bind", obs.DefaultListen, "observability listener in the task action (loopback only)")
		flags.StringVar(&o.dataDir, "data-dir", "", "data directory passed to serve (default: profile data dir)")
		flags.BoolVar(&o.enable, "enable", false, "also run the task now (schtasks /Run)")
		flags.BoolVar(&o.dryRun, "dry-run", false, "print the task XML; write and register nothing")
	}) {
		return 2
	}
	if err := checkInstallBind(o.bind); err != nil {
		sessionsPrintf(stderr, "service install: %s\n", cleanLine(err.Error()))
		return 1
	}
	if o.dataDir != "" {
		if err := checkWinDataDir(o.dataDir); err != nil {
			sessionsPrintf(stderr, "service install: %s\n", cleanLine(err.Error()))
			return 2
		}
	}
	spec := &winTaskSpec{Name: winTaskName(d.profile), Exe: d.base.exe, Bind: o.bind, DataDir: o.dataDir, User: d.user}
	if o.dryRun {
		sessionsPrintf(stdout, "%s", renderTaskXML(spec))
		sessionsPrintf(stdout, "schtasks /Create /TN %s /XML <temp>\\task.xml /F\n", spec.Name)
		return 0
	}
	path, cleanup, err := d.stage(encodeTaskXML(renderTaskXML(spec)))
	if err != nil {
		sessionsPrintf(stderr, "service install: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer func() {
		if cerr := cleanup(); cerr != nil {
			sessionsPrintf(stderr, "service install: %s\n", cleanLine(cerr.Error()))
		}
	}()
	for _, legacy := range winLegacyTaskNames() {
		if _, derr := winDelete(ctx, d, legacy); derr != nil {
			sessionsPrintf(stderr, "service install: legacy task %s: %s\n", legacy, cleanLine(derr.Error()))
		}
	}
	if _, cerr := winSchtasks(ctx, d, "/Create", "/TN", spec.Name, "/XML", path, "/F"); cerr != nil {
		sessionsPrintf(stderr, "service install: %s\n", cleanLine(cerr.Error()))
		return 1
	}
	sessionsPrintf(stdout, "installed task %s\n", spec.Name)
	if !o.enable {
		sessionsPrintf(stdout, "task registered; it starts at next logon (pass --enable to run it now)\n")
		return 0
	}
	if _, rerr := winSchtasks(ctx, d, "/Run", "/TN", spec.Name); rerr != nil {
		sessionsPrintf(stderr, "service install: %s\n", cleanLine(rerr.Error()))
		return 1
	}
	sessionsPrintf(stdout, "started task %s\n", spec.Name)
	return 0
}

// winTaskExists asks the scheduler, not the locale: /Query exits non-zero for
// an absent task, while a missing schtasks binary is reported separately.
func winTaskExists(ctx context.Context, d *winServiceDeps, name string) (bool, error) {
	if _, err := winSchtasks(ctx, d, "/Query", "/TN", name); err != nil {
		if errors.Is(err, errWinToolMissing) {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

// winDelete stops and removes one task; uninstall stays idempotent per exit code.
func winDelete(ctx context.Context, d *winServiceDeps, name string) (bool, error) {
	exists, qerr := winTaskExists(ctx, d, name)
	if qerr != nil || !exists {
		return false, qerr
	}
	_, _ = winSchtasks(ctx, d, "/End", "/TN", name) //nolint:errcheck // a ready task refuses /End; delete still proceeds
	if _, err := winSchtasks(ctx, d, "/Delete", "/TN", name, "/F"); err != nil {
		return true, err
	}
	return true, nil
}

func winUninstallCmd(ctx context.Context, d *winServiceDeps, args []string, stdout, stderr io.Writer) int {
	if !serviceParse("service uninstall", args, stderr, func(*flag.FlagSet) {}) {
		return 2
	}
	names := append([]string{winTaskName(d.profile)}, winLegacyTaskNames()...)
	for _, name := range names {
		existed, err := winDelete(ctx, d, name)
		switch {
		case err != nil:
			sessionsPrintf(stderr, "service uninstall: %s\n", cleanLine(err.Error()))
			return 1
		case existed:
			sessionsPrintf(stdout, "removed task %s\n", name)
		default:
			sessionsPrintf(stdout, "task not present: %s\n", name)
		}
	}
	sessionsPrintf(stdout, "data directories were not touched\n")
	return 0
}

func winStatusCmd(ctx context.Context, d *winServiceDeps, args []string, stdout, stderr io.Writer) int {
	var dataFlag string
	if !serviceParse("service status", args, stderr, func(flags *flag.FlagSet) {
		flags.StringVar(&dataFlag, "data-dir", "", "data directory (default: profile data dir)")
	}) {
		return 2
	}
	data := dataFlag
	if data == "" {
		data = d.base.dataDir
	}
	name := winTaskName(d.profile)
	registered, qerr := winTaskExists(ctx, d, name)
	if qerr != nil {
		sessionsPrintf(stderr, "service status: %s\n", cleanLine(qerr.Error()))
		return 1
	}
	pid := d.leasePID(data)
	state, supervised := winSupervision(pid, registered, d.alive, d.hosted)
	reg, pidText := "no", "-"
	if registered {
		reg = "yes"
	}
	if pid > 0 {
		pidText = strconv.Itoa(pid)
	}
	sessionsPrintf(stdout, "task %s\nregistered %s\nstate %s\nsupervised %s\npid %s\nmode %s\n",
		name, reg, state, supervised, pidText, serviceMode(ctx, d.base, data))
	printWinLedger(stdout, d, data)
	return 0
}

func printWinLedger(w io.Writer, d *winServiceDeps, dataDir string) {
	ledger := serviceLedger(d.base, dataDir)
	sessionsPrintf(w, "ledger %d\n", len(ledger))
	for _, line := range ledger {
		sessionsPrintf(w, "  %s\n", line)
	}
}

// winSupervision answers EC-WIN-003 from evidence: the lease names the serve
// PID; the probe says whether it lives under a scheduler host. A failing probe
// is unknown, never "interactive".
func winSupervision(pid int, registered bool, alive, hosted func(int) (bool, error)) (string, string) {
	if pid <= 0 {
		if registered {
			return "idle", "none"
		}
		return "absent", "none"
	}
	live, err := alive(pid)
	if err != nil {
		return "unknown", "unknown (liveness: " + cleanLine(err.Error()) + ")"
	}
	if !live {
		if registered {
			return "idle", "none (stale lease)"
		}
		return "absent", "none (stale lease)"
	}
	isHosted, herr := hosted(pid)
	switch {
	case herr != nil:
		return "running", "unknown (ancestry: " + cleanLine(herr.Error()) + ")"
	case isHosted:
		return "running", "task-scheduler"
	default:
		return "running", "interactive"
	}
}

// leaseOwnerPID reads the PID serve wrote into run/serve.lock; byte 0 is the
// mandatory lock byte on Windows.
func leaseOwnerPID(raw []byte) int {
	_, rest, ok := strings.Cut(strings.TrimLeft(string(raw), "\x00"), "pid ")
	if !ok {
		return 0
	}
	field := strings.Fields(rest)
	if len(field) == 0 {
		return 0
	}
	pid, err := strconv.Atoi(field[0])
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func winLogsCmd(ctx context.Context, d *winServiceDeps, args []string, stdout, stderr io.Writer) int {
	var dataFlag string
	if !serviceParse("service logs", args, stderr, func(flags *flag.FlagSet) {
		flags.StringVar(&dataFlag, "data-dir", "", "data directory (default: profile data dir)")
	}) {
		return 2
	}
	data := dataFlag
	if data == "" {
		data = d.base.dataDir
	}
	name := winTaskName(d.profile)
	sessionsPrintf(stdout, "task %s (Task Scheduler captures no stdout; task query and crash ledger follow)\n", name)
	if out, err := winSchtasks(ctx, d, "/Query", "/TN", name, "/V", "/FO", "LIST"); err != nil {
		sessionsPrintf(stdout, "query: %s\n", cleanLine(err.Error()))
	} else {
		for line := range strings.SplitSeq(strings.TrimRight(strings.ReplaceAll(out, "\r\n", "\n"), "\n"), "\n") {
			sessionsPrintf(stdout, "  %s\n", line)
		}
	}
	printWinLedger(stdout, d, data)
	return 0
}

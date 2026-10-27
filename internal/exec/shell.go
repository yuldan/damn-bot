package exec

import "os"

// ShellKind is the interpreter family exec uses for a shell call. The
// classifier must follow this choice, not the OS (SEC10-29): Windows can run
// PowerShell, where a cmd-flavoured classification auto-runs payloads such as
// & 'C:\skill\scripts\charge.ps1'.
type ShellKind uint8

// Shell kinds.
const (
	ShellPOSIX ShellKind = iota
	ShellPowerShell
	ShellCmd
)

// shellChoice is one resolved shell: absolute binary and its family.
type shellChoice struct {
	bin  string
	kind ShellKind
}

// commandArgv appends command as the single argument of the chosen shell.
func (c shellChoice) commandArgv(command string) []string {
	switch c.kind {
	case ShellPowerShell:
		return []string{c.bin, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command}
	case ShellCmd:
		return []string{c.bin, "/d", "/s", "/c", command}
	case ShellPOSIX:
		return []string{c.bin, "-c", command}
	default:
		return []string{c.bin, "-c", command}
	}
}

// RuntimeShellKind reports the shell family exec would choose with the process
// environment, so the classifier dialect and the runner cannot disagree
// (SEC10-29).
func RuntimeShellKind() ShellKind {
	c, err := chooseShell(envPath(os.Environ()))
	if err != nil {
		return defaultShellKind()
	}
	return c.kind
}

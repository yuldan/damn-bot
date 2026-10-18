package exec

import (
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// EnvPolicy is the child environment policy (ADR-071, GAP-SEC-02, §A.6):
// deny-by-default inheritance plus explicitly set values.
type EnvPolicy struct {
	// Allow is exec.env_allow: exact names of parent variables inherited
	// besides the GAP-SEC-02 allowlist (no globs).
	Allow []string
	// Set is exec.env with literal values: set for every child and winning
	// over the non-interactive defaults of §A.6 (except hard values).
	Set map[string]string
	// Secrets are exec.env entries resolved from keychain refs.
	Secrets []SecretEnv
	// PassSSHAgent is exec.pass_ssh_agent: inherit SSH_AUTH_SOCK.
	PassSSHAgent bool
}

// SecretEnv is one exec.env secret ref (ADR-081, ADR-088 butir 4): Value
// is injected only into a direct-exec child (mode b) whose argv0 name is in
// Commands; shell-mode children and persistent sessions never get it.
type SecretEnv struct {
	Name     string
	Value    string
	Commands []string
}

// Env builds the child environment from parent ("K=V" entries). direct is
// the argv0 of a mode (b) call ("" for shell mode) and selects the secret
// refs scoped to it; pty selects TERM.
func (p *EnvPolicy) Env(parent []string, pty bool, direct string) []string {
	out := make(map[string]string, len(parent))
	for _, kv := range parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || !p.inherits(k) {
			continue
		}
		if isProxyVar(k) {
			if v, ok = stripUserinfo(v); !ok {
				continue // unparsable proxy URL that may hold credentials
			}
		}
		out[envKey(k)] = k + "=" + v
	}
	set := func(k, v string) { out[envKey(k)] = k + "=" + v }
	for _, kv := range nonInteractive(pty) {
		set(kv[0], kv[1])
	}
	if _, user := p.Set["GIT_SSH_COMMAND"]; !user && !hasKey(out, "GIT_SSH_COMMAND") {
		set("GIT_SSH_COMMAND", "ssh -o BatchMode=yes")
	}
	for k, v := range p.Set {
		set(k, v)
	}
	if direct != "" {
		name := commandName(direct)
		for _, s := range p.Secrets {
			if slices.ContainsFunc(s.Commands, func(c string) bool { return commandName(c) == name }) {
				set(s.Name, s.Value)
			}
		}
	}
	set("GIT_TERMINAL_PROMPT", "0") // hard values (§A.6): never overridden
	delete(out, envKey(MarkerEnv))  // the runner sets the marker and DAMNBOT
	delete(out, envKey("DAMNBOT"))
	env := make([]string, 0, len(out))
	for _, kv := range out {
		env = append(env, kv)
	}
	slices.Sort(env)
	return env
}

// nonInteractive is the §A.6 table: pagers, editors and prompts fail or
// pass through instead of waiting on a terminal.
func nonInteractive(pty bool) [][2]string {
	term := "dumb"
	if pty {
		term = "xterm-256color"
	}
	return [][2]string{
		{"PAGER", "cat"},
		{"GIT_PAGER", "cat"},
		{"MANPAGER", "cat"},
		{"SYSTEMD_PAGER", "cat"},
		{"CI", "1"},
		{"NONINTERACTIVE", "1"},
		{"PIP_NO_INPUT", "1"},
		{"DEBIAN_FRONTEND", "noninteractive"},
		{"EDITOR", "false"},
		{"VISUAL", "false"},
		{"GIT_EDITOR", "false"},
		{"GIT_SEQUENCE_EDITOR", "false"},
		{"GIT_MERGE_AUTOEDIT", "no"},
		{"PYTHONUNBUFFERED", "1"},
		{"TERM", term},
	}
}

// inherits reports whether parent variable k passes GAP-SEC-02 or
// exec.env_allow.
func (p *EnvPolicy) inherits(k string) bool {
	if slices.ContainsFunc(p.Allow, func(a string) bool { return envKey(a) == envKey(k) }) {
		return true
	}
	if envKey(k) == envKey("SSH_AUTH_SOCK") {
		return p.PassSSHAgent
	}
	u := strings.ToUpper(k)
	if slices.Contains(baseAllow(), u) || isProxyVar(k) {
		return true
	}
	if runtime.GOOS == "windows" && slices.Contains(windowsAllow(), u) {
		return true
	}
	switch {
	case strings.HasPrefix(u, "LC_"), strings.HasPrefix(u, "XDG_"):
		return true
	case strings.HasSuffix(u, "_HOME"):
		return !secretName(u)
	}
	return false
}

// baseAllow is the GAP-SEC-02 list (TERM is set per runner, §A.6).
func baseAllow() []string {
	return []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "COLORTERM", "TZ", "TMPDIR",
		"GOPATH", "GOROOT", "GOCACHE", "CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "NVM_DIR",
		"PYENV_ROOT", "VIRTUAL_ENV", "CONDA_PREFIX",
	}
}

// windowsAllow are the Windows counterparts of HOME/USER/TMPDIR and the
// variables a Win32 process needs to start (ADR-105 butir 13).
func windowsAllow() []string {
	return []string{
		"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP",
		"USERPROFILE", "USERNAME", "USERDOMAIN", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA",
		"PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMW6432", "COMMONPROGRAMFILES",
		"COMMONPROGRAMFILES(X86)", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "OS",
	}
}

// secretName is the SPEC §14.3 secret-name rule, applied to *_HOME names.
func secretName(u string) bool {
	for _, w := range [...]string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "AUTH"} {
		if strings.Contains(u, w) {
			return true
		}
	}
	return false
}

func isProxyVar(k string) bool {
	switch strings.ToUpper(k) {
	case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY":
		return true
	}
	return false
}

// stripUserinfo drops user:password from a proxy URL; ok is false when v
// looks like it carries userinfo but does not parse.
func stripUserinfo(v string) (string, bool) {
	if !strings.Contains(v, "@") {
		return v, true
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "", false
	}
	u.User = nil
	return u.String(), true
}

// envKey is the identity of a variable name: case-insensitive on Windows.
func envKey(k string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(k)
	}
	return k
}

func hasKey(env map[string]string, k string) bool {
	_, ok := env[envKey(k)]
	return ok
}

// commandName is the name argv0 is matched by in SecretEnv.Commands: the
// base name, without .exe and case-insensitive on Windows.
func commandName(argv0 string) string {
	n := filepath.Base(argv0)
	if runtime.GOOS == "windows" {
		n = strings.TrimSuffix(strings.ToLower(n), ".exe")
	}
	return n
}

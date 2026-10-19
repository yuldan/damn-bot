package exec

import (
	"net/url"
	"os"
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
// is injected only into a direct-exec child (mode b) that resolves to the
// same binary as one of Commands (absolute path plus dev/inode/size/mtime);
// shell-mode children and persistent sessions never get it (EX42-1).
type SecretEnv struct {
	Name     string
	Value    string
	Commands []string
}

// Env builds the child environment from parent ("K=V" entries). pty
// selects TERM. direct is the resolved absolute argv0 of a mode (b) call
// ("" for shell mode) whose binary identity selects the secret refs; dir
// resolves configured commands that contain a path separator and root, when
// set, is the workspace root: a secret command resolving inside it is
// never injected (EX42-1).
func (p *EnvPolicy) Env(parent []string, pty bool, direct, dir, root string) []string {
	out := make(map[string]string, len(parent))
	p.inheritInto(out, parent)
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
		p.injectSecrets(out, direct, dir, root)
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

// Path returns parent's PATH value under this policy ("" when PATH is not
// inherited), i.e. the PATH used to resolve argv0 before the child env is
// built.
func (p *EnvPolicy) Path(parent []string) string {
	if !p.inherits("PATH") {
		return ""
	}
	return lookupEnv(parent, "PATH")
}

// inheritInto copies the allowed parent variables into out.
func (p *EnvPolicy) inheritInto(out map[string]string, parent []string) {
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
}

// injectSecrets adds the secret refs whose command resolves to the spawned
// binary (mode b only).
func (p *EnvPolicy) injectSecrets(out map[string]string, direct, dir, root string) {
	bin, ok := identify(direct)
	if !ok {
		return
	}
	path := envValue(out, "PATH")
	for _, s := range p.Secrets {
		if p.secretMatches(s, bin, path, dir, root) {
			out[envKey(s.Name)] = s.Name + "=" + s.Value
		}
	}
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
		// less shows the file name, not a prompt, on its first screen; a
		// fixed "--More--" prompt makes it a detectable pager (ADR-105 butir 17).
		{"LESS", "-Ps--More--"},
	}
}

// inherits reports whether parent variable k passes GAP-SEC-02 or
// exec.env_allow. Names are exact on Unix; only Windows folds case
// (GAP-SEC-02; EX42-4).
func (p *EnvPolicy) inherits(k string) bool {
	n := envKey(k)
	if slices.ContainsFunc(p.Allow, func(a string) bool { return envKey(a) == n }) {
		return true
	}
	if n == envKey("SSH_AUTH_SOCK") {
		return p.PassSSHAgent
	}
	if slices.Contains(baseAllow(), n) || isProxyVar(k) {
		return true
	}
	if runtime.GOOS == "windows" && slices.Contains(windowsAllow(), n) {
		return true
	}
	switch {
	case strings.HasPrefix(n, "LC_"), strings.HasPrefix(n, "XDG_"):
		return true
	case strings.HasSuffix(n, "_HOME"):
		return !secretName(strings.ToUpper(n))
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
	// Exact names on Unix; envKey folds case on Windows only (GAP-SEC-02).
	switch envKey(k) {
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

// envValue returns the value of k in a filtered env map ("K=V").
func envValue(env map[string]string, k string) string {
	if kv, ok := env[envKey(k)]; ok {
		_, v, _ := strings.Cut(kv, "=")
		return v
	}
	return ""
}

// BinaryID is a resolved executable identity (ADR-088 butir 3): absolute
// path and file identity. Secret refs match by identity, never by base
// name, so "./gh" or a same-named workspace PATH entry cannot receive a
// secret aimed at another binary (EX42-1).
type BinaryID struct {
	path string
	fi   os.FileInfo
}

// identify stats path and normalizes it for comparison.
func identify(path string) (BinaryID, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return BinaryID{}, false
	}
	p := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return BinaryID{path: p, fi: fi}, true
}

// same reports whether b and o are the same file at the same path with the
// same size and mtime (ADR-088 butir 3).
func (b BinaryID) same(o BinaryID) bool {
	return b.path == o.path && os.SameFile(b.fi, o.fi) &&
		b.fi.Size() == o.fi.Size() && b.fi.ModTime().Equal(o.fi.ModTime())
}

// secretMatches resolves each configured command through the child PATH
// and compares its identity with the spawned binary (EX42-1). A command
// with a path separator is ignored: the scope is a bare command name
// resolved by the session, as in shell_allow (ADR-088 butir 3). A binary
// inside the workspace root never gets a secret (NEW3-04): a workspace
// PATH entry must not shadow the intended command.
func (p *EnvPolicy) secretMatches(s SecretEnv, bin BinaryID, path, dir, root string) bool {
	for _, c := range s.Commands {
		if strings.ContainsAny(c, `/\`) {
			continue
		}
		id, ok := resolveBinary(c, path, dir)
		if ok && !withinRoot(id.path, root) && id.same(bin) {
			return true
		}
	}
	return false
}

// withinRoot reports whether path is inside root (root "" = no check).
func withinRoot(path, root string) bool {
	if root == "" {
		return false
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	rel, err := filepath.Rel(filepath.Clean(root), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveBinary resolves name exactly like mode (b) does and returns its
// identity.
func resolveBinary(name, path, dir string) (BinaryID, bool) {
	p, err := lookPath(name, path, dir)
	if err != nil {
		return BinaryID{}, false
	}
	return identify(p)
}

// lookupEnv returns the last value of key in an env slice, or "".
func lookupEnv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

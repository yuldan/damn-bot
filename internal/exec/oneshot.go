package exec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// OneShot is one `shell` call without a persistent session (§A.2).
type OneShot struct {
	Command string
	// Direct is mode (b): the argv of a single literal simple command
	// (classify.DirectArgv), executed without an interpreter. nil = mode
	// (a), the command as one argument of `bash -c` (sh, pwsh, cmd).
	Direct    []string
	Dir       string
	Stdin     []byte
	PTY       bool
	Timeout   time.Duration
	KillGrace time.Duration
	Parent    []string // parent environment ("K=V")
	Policy    *EnvPolicy
}

// ErrNotFound reports that argv0 (or any shell) does not resolve through
// the child's PATH.
var ErrNotFound = errors.New("exec: command not found")

// Prepare turns o into a Spec. argv0 resolves through the child's PATH
// (absolute entries only). notice is non-empty when a secret ref scoped
// to the command is not injected because the command is not a single
// literal simple command (NEW3-03, ADR-105 butir 14): it runs in mode (a)
// without the secret.
func Prepare(o *OneShot) (spec Spec, notice string, err error) {
	policy := o.Policy
	if policy == nil {
		policy = &EnvPolicy{}
	}
	direct := ""
	if len(o.Direct) > 0 {
		direct = o.Direct[0]
	}
	env := policy.Env(o.Parent, o.PTY, direct)
	path := envPath(env)
	var argv []string
	if direct != "" {
		bin, lerr := lookPath(direct, path, o.Dir)
		if lerr != nil {
			return Spec{}, "", lerr
		}
		argv = append([]string{bin}, o.Direct[1:]...)
	} else {
		if argv, err = shellArgv(o.Command, path); err != nil {
			return Spec{}, "", err
		}
		notice = secretNotice(o.Command, policy.Secrets)
	}
	return Spec{
		Argv: argv, Dir: o.Dir, Env: env, Stdin: o.Stdin, PTY: o.PTY,
		Timeout: o.Timeout, KillGrace: o.KillGrace,
	}, notice, nil
}

// shellArgv is mode (a): bash, else sh (Unix); pwsh, powershell, else cmd
// (Windows), with command as a single argument.
func shellArgv(command, path string) ([]string, error) {
	type shell struct {
		name string
		args []string
	}
	shells := []shell{{"bash", []string{"-c"}}, {"sh", []string{"-c"}}}
	if runtime.GOOS == "windows" {
		shells = []shell{
			{"pwsh", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}},
			{"powershell", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}},
			{"cmd", []string{"/d", "/s", "/c"}},
		}
	}
	for _, sh := range shells {
		if bin, err := lookPath(sh.name, path, ""); err == nil {
			return append(append([]string{bin}, sh.args...), command), nil
		}
	}
	return nil, fmt.Errorf("%w: no shell on PATH", ErrNotFound)
}

// lookPath resolves name like a shell, but only through absolute PATH
// entries: an empty or relative entry would resolve against the working
// directory, i.e. inside the workspace (NEW3-04). A name with a separator
// is taken as is, relative to dir. On Windows only .exe and .com run
// (CreateProcess would start cmd.exe for .bat/.cmd, ADR-088).
func lookPath(name, path, dir string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if executable(p) {
			return name, nil
		}
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	for _, d := range filepath.SplitList(path) {
		if !filepath.IsAbs(d) {
			continue
		}
		for _, cand := range candidates(filepath.Join(d, name)) {
			if executable(cand) {
				return cand, nil
			}
		}
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, name)
}

func candidates(p string) []string {
	if runtime.GOOS != "windows" {
		return []string{p}
	}
	ext := strings.ToLower(filepath.Ext(p))
	if ext == ".exe" || ext == ".com" {
		return []string{p}
	}
	return []string{p + ".exe", p + ".com"}
}

// executable: a regular file (Unix: with an execute bit; Windows: .exe or
// .com).
func executable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(p))
		return ext == ".exe" || ext == ".com"
	}
	return fi.Mode().Perm()&0o111 != 0
}

// envPath returns PATH from env ("K=V" entries; case-insensitive name on
// Windows).
func envPath(env []string) string {
	i := slices.IndexFunc(env, func(kv string) bool {
		n, _, _ := strings.Cut(kv, "=")
		return envKey(n) == envKey("PATH")
	})
	if i < 0 {
		return ""
	}
	_, v, _ := strings.Cut(env[i], "=")
	return v
}

// secretNotice explains secret refs a shell-mode command refers to ($NAME
// or ${NAME…}) that the child will not receive.
func secretNotice(command string, secrets []SecretEnv) string {
	var names []string
	for _, s := range secrets {
		if strings.Contains(command, "$"+s.Name) || strings.Contains(command, "${"+s.Name) {
			names = append(names, s.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "secret env " + strings.Join(names, ", ") +
		" not injected: only a single literal command run without a shell receives it"
}

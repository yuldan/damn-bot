//go:build unix

package exec

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestRunIdleTimeout (§A.4): a silent sleeping tree is killed after the
// idle window; a silent CPU-busy one runs until its timeout.
func TestRunIdleTimeout(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("no tree accounting: never idle-killed (ADR-105 butir 16)")
	}
	s := sh(t, `echo start; sleep 30`)
	s.IdleTimeout, s.KillGrace = time.Second, time.Second
	res := run(t.Context(), t, newTestRunner(t), s)
	if res.Status != StatusIdleTimeout || res.Duration > 5*time.Second {
		t.Fatalf("sleep: %s after %v", res.Status, res.Duration)
	}
	busy := sh(t, `while :; do :; done`)
	busy.IdleTimeout, busy.Timeout, busy.KillGrace = time.Second, 3*time.Second, time.Second
	if res := run(t.Context(), t, newTestRunner(t), busy); res.Status != StatusTimeout {
		t.Fatalf("busy loop: %s after %v", res.Status, res.Duration)
	}
}

// interactiveFixture is one TestInteractiveCorpus case: a script, or a
// real program when present (skipped otherwise), and the kind expected
// on the PTY runner ("" = exits on its own).
type interactiveFixture struct {
	name, script, kind string
	linuxOnly          bool
}

func interactiveFixtures(t *testing.T) []interactiveFixture {
	t.Helper()
	repo := t.TempDir()
	git := `cd ` + repo + ` && git init -q . && git -c user.name=t -c user.email=t@t commit -q --allow-empty -m init && `
	fs := []interactiveFixture{
		{"apt", `echo 'The following NEW packages will be installed'; printf 'Do you want to continue? [Y/n] '; read a`, InputConfirm, false},
		{"ssh-hostkey", `printf 'Are you sure you want to continue connecting (yes/no/[fingerprint])? '; read a`, InputConfirm, false},
		{"sudo", `printf '[sudo] password for dev: '; read a`, InputSecret, false},
		{"git-username", `printf "Username for 'https://github.com': "; read a`, InputText, false},
		{"mysql-p", `printf 'Enter password: '; read a`, InputSecret, false},
		{"ssh-passphrase", `printf "Enter passphrase for key '/home/u/.ssh/id_ed25519': "; read a`, InputSecret, false},
		{"pin", `printf 'PIN: '; read a`, InputSecret, false},
		{"token", `printf 'Paste your API token here: '; read a`, InputSecret, false},
		{"overwrite-yn", `printf 'Overwrite file? (y/n) '; read a`, InputConfirm, false},
		{"yesno-bracket", `printf 'Proceed [yes/no]: '; read a`, InputConfirm, false},
		{"press-enter", `printf 'Press ENTER to continue'; read a`, InputText, false},
		{"press-any-key", `printf 'Press any key to exit...'; read a`, InputText, false},
		{"question", `printf 'What is your name? '; read a`, InputText, false},
		{"colon", `printf 'Name: '; read a`, InputText, false},
		{"more", `printf -- '--More--(50%%)'; read a`, InputPager, false},
		{"end", `printf '(END)'; read a`, InputPager, false},
		{"less-colon", `printf ':'; read a`, InputPager, false},
		{"ansi", `printf '\033[1mContinue? [y/N]\033[0m '; read a`, InputConfirm, false},
		{"cr-redraw", `printf 'downloading 50%%\rReplace existing? [y/n] '; read a`, InputConfirm, false},
		{"after-output", `i=0; while [ $i -lt 50 ]; do echo line $i; i=$((i+1)); done; printf 'Delete these? [y/N] '; read a`, InputConfirm, false},
		{"ignores-term", `trap '' TERM; printf 'Continue? [y/n] '; read a`, InputConfirm, false},
		{"background-child", `sleep 100 & printf 'ok? (yes/no) '; read a`, InputConfirm, false},
		{"otp", `printf 'Verification code: '; read a`, InputText, false},
		{"retype", `printf 'Retype new password: '; read a`, InputSecret, false},
		{"type-yes", `printf "Type 'yes' to confirm: "; read a`, InputText, false},
		{"menu", `echo '1) a'; echo '2) b'; printf 'Select an option [1-2]: '; read a`, InputText, false},
		{"login", `printf 'login: '; read a`, InputText, false},
		{"ssh-keygen", `printf 'Enter file in which to save the key (/home/u/.ssh/id_ed25519): '; read a`, InputText, false},
		{"npm-init", `printf 'package name: (app) '; read a`, InputText, true}, // no pattern: wchan
		{"silent-read", `read a`, InputText, true},                             // no output at all: wchan
		{"exits", `echo done`, "", false},
		{"fails", `echo boom >&2; exit 3`, "", false},
		{"python-input", `exec python3 -c 'input("Name: ")'`, InputText, false},
		{"python-getpass", `exec python3 -c 'import getpass; getpass.getpass()'`, InputSecret, false},
		{"git-commit-no-m", git + `echo x > f && git add f && git -c user.name=t -c user.email=t@t commit -q`, "", false},
		{"git-rebase-i", git + `git -c user.name=t -c user.email=t@t commit -q --allow-empty -m two && git rebase -i HEAD~1`, "", false},
		{"less", `seq 1 200 > /tmp/dmb-less-$$ 2>/dev/null || i=0; exec less /tmp/dmb-less-$$`, InputPager, false},
		{"git-log-pager", git + `git log`, "", false}, // GIT_PAGER=cat
		{"man", `exec man -P cat ls`, "", false},
		{"vi", `exec vi /tmp/dmb-vi-$$`, InputText, true},
	}
	return fs
}

// TestInteractiveCorpus (P24.2, §A.6): every fixture returns quickly with
// status exited or waiting_for_input — pipe (stdin = /dev/null fails
// fast) and PTY (prompt detected, tree killed).
//
// Not parallel: the wchan fixtures all reach the 5 s silence mark together,
// and each Runner scans /proc once a second; running the corpus alongside
// itself (and TestNoOrphans does not run parallel either) made those scans
// contend and pushed vi/pty past its 8 s bound on CI (P24.2).
func TestInteractiveCorpus(t *testing.T) {
	for _, f := range interactiveFixtures(t) {
		for _, pty := range []bool{false, true} {
			t.Run(f.name+map[bool]string{false: "/pipe", true: "/pty"}[pty], func(t *testing.T) {
				skipFixture(t, f, pty)
				res := run(t.Context(), t, newTestRunner(t), fixtureSpec(t, f, pty))
				if msg := checkFixture(f, pty, &res); msg != "" {
					t.Fatalf("%s (status %s, kind %q, prompt %q, after %v)", msg, res.Status, res.InputKind, res.Prompt, res.Duration)
				}
				assertNoMarker(t, res.ExecID)
			})
		}
	}
}

func skipFixture(t *testing.T, f interactiveFixture, pty bool) {
	t.Helper()
	if f.linuxOnly && pty && runtime.GOOS != "linux" {
		t.Skip("detection without a prompt line needs /proc wchan")
	}
	if f.name == "vi" && !pty {
		t.Skip("tty_required: NeedsTTY always selects the PTY runner for vi")
	}
	need := map[string]string{
		"python-input": "python3", "python-getpass": "python3", "git-commit-no-m": "git",
		"git-rebase-i": "git", "git-log-pager": "git", "less": "less", "man": "man", "vi": "vi",
	}[f.name]
	if need == "" {
		return
	}
	if _, err := exec.LookPath(need); err != nil {
		t.Skip("no " + need)
	}
}

func fixtureSpec(t *testing.T, f interactiveFixture, pty bool) *Spec {
	t.Helper()
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	env := (&EnvPolicy{}).Env([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, pty, "", "", "")
	return &Spec{
		Argv: []string{shPath, "-c", f.script}, Dir: t.TempDir(), PTY: pty, Env: env,
		Timeout: 20 * time.Second, KillGrace: time.Second,
	}
}

// checkFixture returns why res breaks P24.2 for f ("" = ok).
func checkFixture(f interactiveFixture, pty bool, res *Result) string {
	switch {
	case res.Status != StatusExited && res.Status != StatusSignaled && res.Status != StatusWaitingForInput:
		return "status not in {exited, waiting_for_input}"
	case !pty && res.Duration > 3*time.Second:
		return "pipe did not fail fast"
	case pty && f.kind != "" && (res.Status != StatusWaitingForInput || res.InputKind != f.kind):
		return "pty: want waiting_for_input " + f.kind
	case pty && res.Status == StatusWaitingForInput && res.Duration > limitFor(f):
		return "pty: returned too late after the prompt"
	}
	return ""
}

// limitFor is P24.2 for a fixture: ≤ 2 s after the prompt appears (the
// prompt is printed at once; + kill_grace for a TERM-ignoring tree), or the
// wchan path's 5 s silence.
func limitFor(f interactiveFixture) time.Duration {
	if f.linuxOnly {
		return wchanSilence + 3*time.Second
	}
	return 2*time.Second + time.Second + 500*time.Millisecond
}

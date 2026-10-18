package classify

import (
	"errors"
	"strings"
)

const (
	testRoot = "/home/u/proj"
	testHome = "/home/u"
)

// testDeny is SPEC §12's default deny_paths.
func testDeny() []string {
	return []string{
		"~/.ssh", "~/.gnupg", "~/.aws", "**/.env", "**/*.pem", "~/.netrc", "~/.git-credentials",
		"~/.config/gh", "~/.docker/config.json", "~/.kube", "/proc/*/environ", "/proc/*/task/*/environ", "/proc/*/mem",
	}
}

func testEnv() *Env {
	return &Env{
		Roots:         []string{testRoot},
		Home:          testHome,
		BlobDir:       "/home/u/.local/share/damnbot/blobs",
		DenyPaths:     testDeny(),
		SecretVars:    []string{"STRIPE_KEY"},
		MonetaryHosts: []string{"api.stripe.com"},
	}
}

// fakeFS is a PathResolver over a symlink table and a fixed glob listing.
type fakeFS struct {
	links map[string]string
	globs map[string][]string
}

var errNoGlob = errors.New("no match")

func (f fakeFS) EvalSymlinks(p string) (string, error) {
	for from, to := range f.links {
		if p == from || strings.HasPrefix(p, from+"/") {
			return to + p[len(from):], nil
		}
	}
	return p, nil
}

func (f fakeFS) Glob(p string) ([]string, error) {
	if m, ok := f.globs[p]; ok {
		return m, nil
	}
	return nil, errNoGlob
}

// modes are the table columns: ask, auto-edit, auto, headless, headless
// --approval auto --unsafe-auto-shell.
func modes() [5]Mode {
	return [5]Mode{ModeAsk, ModeAutoEdit, ModeAuto, ModeHeadless, ModeUnsafeAutoShell}
}

// decisionCodes: Q ask, P ask+preview, C ask per command, A auto, D denied,
// E denied_path error, O outside_workspace, U unverified.
func decisionCode(d Decision) byte { return "QPCADEOU"[d] }

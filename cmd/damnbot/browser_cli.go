//go:build !minimal

package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/damnbot/damnbot/internal/browser"
	"github.com/damnbot/damnbot/internal/modules/host"
)

func browserSurface() []surfaceEntry {
	return []surfaceEntry{{Name: "browser", Summary: "Real browser profiles: close the one locking a profile", Phase: phaseF6, Run: runBrowser, Usage: browserUsage}}
}

func runBrowser(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "close-profile" {
		if len(args) > 0 {
			sessionsPrintf(stderr, "browser: unknown subcommand %q\n", cleanArg(args[0]))
		}
		browserUsage(stderr)
		return 2
	}
	return runCloseProfile(args[1:], stdout, stderr)
}

func browserUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot browser close-profile <profile> --yes\n")
	sessionsPrintf(w, "profiles: chrome, chromium, brave, edge. Without --yes nothing closes (exit 4).\n")
}

func runCloseProfile(args []string, stdout, stderr io.Writer) int {
	yes, names := false, make([]string, 0, 1)
	for _, arg := range args {
		switch {
		case arg == "--yes":
			yes = true
		case strings.HasPrefix(arg, "-"):
			browserUsage(stderr)
			return 2
		default:
			names = append(names, arg)
		}
	}
	if len(names) != 1 {
		browserUsage(stderr)
		return 2
	}
	if err := approveCloseProfile(names[0], yes, stdout); err != nil {
		sessionsPrintf(stderr, "browser close-profile: %s\n", cleanLine(err.Error()))
		return 4
	}
	res, err := browser.CloseProfile(names[0], browser.DefaultProfiles(), browser.SystemProfileHooks())
	if err != nil {
		sessionsPrintf(stderr, "browser close-profile: %s\n", cleanLine(err.Error()))
		if errors.Is(err, browser.ErrInvalidProfile) {
			return 2
		}
		return 3
	}
	if res.AlreadyClosed {
		sessionsPrintf(stdout, "browser profile %s is already closed\n", res.Name)
	} else {
		sessionsPrintf(stdout, "browser profile %s closed (pid %d)\n", res.Name, res.PID)
	}
	return 0
}

func approveCloseProfile(name string, yes bool, out io.Writer) error {
	req := &host.ApprovalRequest{
		Tool: "browser_close_profile", Effect: host.EffectDestructive,
		Class: "browser:real_profile:" + name, Preview: "close the real browser profile " + name,
	}
	if err := req.Validate(); err != nil {
		return fmt.Errorf("approval request: %w", err)
	}
	sessionsPrintf(out, "approval: %s (%s)\n%s\n", req.Tool, req.Class, cleanLine(req.Preview))
	if !yes {
		return errors.New("denied: re-run with --yes to close the profile")
	}
	return nil
}

// Host detector for skill requirements (GAP-TOOL-10, SPEC §22). It is explicit
// on purpose: every check is an observation, and a check that cannot be made
// returns an error so the requirement is reported as unmet instead of passing
// silently (the gpu case is the one that used to have no detector at all).
package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/skills"
)

// skillVersionTimeout bounds one `<bin> --version` probe; the detector never
// runs a binary without a deadline.
const skillVersionTimeout = 2 * time.Second

// skillHost returns the host facts for skill requirements.
func skillHost() skills.Host {
	return skills.Host{
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		LookPath: exec.LookPath,
		Version:  binaryVersion,
		GPU:      detectGPU,
	}
}

// binaryVersion runs `<name> --version` with a short deadline and returns the
// first version-looking token. No shell, no arguments from the skill.
func binaryVersion(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), skillVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", name, err)
	}
	for field := range strings.FieldsSeq(string(out)) {
		field = strings.TrimPrefix(field, "v")
		if field == "" {
			continue
		}
		if field[0] >= '0' && field[0] <= '9' {
			return field, nil
		}
	}
	return "", fmt.Errorf("%s --version printed no version", name)
}

// detectGPU reports whether a usable GPU is present. Only Linux has a
// detector (a DRM render node); on every other platform the answer is "cannot
// verify", which the evaluator reports as unmet.
func detectGPU() (bool, error) {
	if runtime.GOOS != "linux" {
		return false, fmt.Errorf("no GPU detector on %s", runtime.GOOS)
	}
	matches, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil {
		return false, fmt.Errorf("gpu probe: %w", err)
	}
	return len(matches) > 0, nil
}

// whyReasons renders the unmet requirements of one skill on a single line.
func whyReasons(reasons []skills.Reason) string {
	parts := make([]string, len(reasons))
	for i := range reasons {
		parts[i] = skillsArg(reasons[i].String())
	}
	return strings.Join(parts, "; ")
}

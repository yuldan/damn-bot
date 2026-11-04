package browser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// ErrInvalidProfile marks a profile name outside the closed catalog.
var ErrInvalidProfile = errors.New("browser: unknown profile")

// Profile is one Chromium-family profile directory.
type Profile struct{ Name, Dir string }

// DefaultProfiles returns the closed profile catalog of this host.
func DefaultProfiles() []Profile {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = filepath.Join(home, ".config")
	}
	var base string
	names := []string{"Google/Chrome", "Chromium", "BraveSoftware/Brave-Browser", "Microsoft Edge"}
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base, names = filepath.Join(home, "AppData", "Local"), []string{"Google/Chrome/User Data", "Chromium/User Data", "BraveSoftware/Brave-Browser/User Data", "Microsoft/Edge/User Data"}
	default:
		base, names = configDir, []string{"google-chrome", "chromium", "BraveSoftware/Brave-Browser", "microsoft-edge"}
	}
	out := make([]Profile, 0, 4)
	for i, name := range []string{"chrome", "chromium", "brave", "edge"} {
		out = append(out, Profile{name, filepath.Join(base, filepath.FromSlash(names[i]))})
	}
	return out
}

// ProfileHooks are the OS operations CloseProfile needs.
type ProfileHooks struct {
	LockPID func(dir string) (int, error)
	Owns    func(pid int, dir string) (bool, error)
	Stop    func(pid int) error
}

// ProfileResult is the outcome of one close-profile call.
type ProfileResult struct {
	Name, Dir             string
	PID                   int
	Closed, AlreadyClosed bool
}

// CloseProfile closes only the browser holding the named profile's lock.
func CloseProfile(name string, profiles []Profile, hooks ProfileHooks) (ProfileResult, error) {
	var profile Profile
	found := false
	for _, p := range profiles {
		if p.Name == name {
			profile, found = p, true
			break
		}
	}
	if !found {
		return ProfileResult{}, fmt.Errorf("%w: %q", ErrInvalidProfile, name)
	}
	res := ProfileResult{Name: profile.Name, Dir: profile.Dir}
	if hooks.LockPID == nil || hooks.Owns == nil || hooks.Stop == nil {
		return res, fmt.Errorf("%w: close-profile is unavailable on this platform", errUnavailable)
	}
	pid, err := hooks.LockPID(profile.Dir)
	switch {
	case errors.Is(err, os.ErrNotExist), err == nil && pid <= 0:
		res.AlreadyClosed = true
		return res, nil
	case err != nil:
		return res, fmt.Errorf("%w: read profile lock: %w", errUnavailable, err)
	}
	res.PID = pid
	owns, err := hooks.Owns(pid, profile.Dir)
	if err != nil {
		return res, fmt.Errorf("%w: inspect profile lock: %w", errUnavailable, err)
	}
	if !owns {
		return res, fmt.Errorf("%w: profile lock does not belong to %s", errDenied, profile.Name)
	}
	if err := hooks.Stop(pid); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			res.AlreadyClosed = true
			return res, nil
		}
		return res, fmt.Errorf("%w: close %s: %w", errUnavailable, profile.Name, err)
	}
	res.Closed = true
	return res, nil
}

// SystemProfileHooks returns the Linux implementation; other platforms fail closed.
func SystemProfileHooks() ProfileHooks {
	if runtime.GOOS != "linux" {
		return ProfileHooks{
			LockPID: func(string) (int, error) { return 0, errUnavailable },
			Owns:    func(int, string) (bool, error) { return false, errUnavailable },
			Stop:    func(int) error { return errUnavailable },
		}
	}
	return ProfileHooks{LockPID: profileLockPID, Owns: profileOwned, Stop: stopProfile}
}

func profileLockPID(dir string) (int, error) {
	lock := filepath.Join(dir, "SingletonLock")
	if target, err := os.Readlink(lock); err == nil {
		if _, pidText, ok := strings.Cut(target, "-"); ok {
			if pid, perr := strconv.Atoi(pidText); perr == nil {
				return pid, nil
			}
		}
		return 0, fmt.Errorf("%w: malformed SingletonLock", errUnavailable)
	}
	data, err := os.ReadFile(lock) // #nosec G304 -- profile directory from the closed catalog
	if err != nil {
		return 0, fmt.Errorf("%w: read profile lock: %w", errUnavailable, err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("%w: empty SingletonLock", errUnavailable)
	}
	pid, perr := strconv.Atoi(fields[len(fields)-1])
	if perr != nil {
		return 0, fmt.Errorf("%w: malformed SingletonLock", errUnavailable)
	}
	return pid, nil
}

func profileOwned(pid int, dir string) (bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline") // #nosec G304 -- pid from the profile lock
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("%w: read process %d: %w", errUnavailable, pid, err)
	}
	args := strings.Split(string(raw), "\x00")
	if len(args) == 0 || args[0] == "" {
		return false, nil
	}
	switch strings.ToLower(filepath.Base(args[0])) {
	case "chrome", "chromium", "chromium-browser", "brave", "brave-browser", "msedge", "microsoft-edge":
	default:
		return false, nil
	}
	want := "--user-data-dir=" + dir
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "--user-data-dir=") {
			return arg == want, nil
		}
	}
	return true, nil
}

func stopProfile(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("%w: find process %d: %w", errUnavailable, pid, err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("%w: signal process %d: %w", errUnavailable, pid, err)
	}
	return nil
}

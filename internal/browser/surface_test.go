//go:build !minimal

package browser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func envFor(t *testing.T, id string) host.Env {
	t.Helper()
	view, err := host.NewSessionView(id, "", "test", "", nil, nil)
	if err != nil {
		t.Fatalf("session view: %v", err)
	}
	return host.Env{Session: view}
}

func writeManifest(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func surfaceConfig(t *testing.T) ModuleConfig {
	t.Helper()
	return moduleConfig()
}

// TestF6EXEC040RealProfileApproval (EXEC-040): a real profile is opt-in, the
// approval is scoped to one session and one profile, and a default profile
// never touches a browser.
func TestF6EXEC040RealProfileApproval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"negative control: real_profile off denies without an approval", realProfileDenied},
		{"allow_session is per session and per profile", realProfileGrants},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

func realProfileDenied(t *testing.T) {
	t.Helper()
	f := newFake()
	var dials int
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := surfaceConfig(t)
	cfg.RealProfile = false
	m := newModule(t.Context(), t, &cfg, Deps{
		Dial: func(context.Context, string, time.Duration) (Transport, error) {
			dials++
			return f, nil
		},
		Approvals: appr,
	})
	_, err := m.Connect(t.Context(), "cdp", "default", envFor(t, "s1"))
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" {
		t.Fatalf("err = %v, want denied", err)
	}
	if len(appr.requests()) != 0 || dials != 0 {
		t.Fatalf("approvals = %d, dials = %d, want 0 and 0", len(appr.requests()), dials)
	}
}

func realProfileGrants(t *testing.T) {
	t.Helper()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalAllowSession}
	cfg := surfaceConfig(t)
	cfg.RealProfile = true
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	if _, err := m.Connect(t.Context(), "cdp", "work", envFor(t, "s1")); err != nil {
		t.Fatalf("connect work/s1: %v", err)
	}
	if len(appr.requests()) != 1 {
		t.Fatalf("approvals = %d, want 1", len(appr.requests()))
	}
	if _, err := m.Connect(t.Context(), "cdp", "work", envFor(t, "s1")); err != nil {
		t.Fatalf("second connect work/s1: %v", err)
	}
	if len(appr.requests()) != 1 {
		t.Fatalf("same session/profile asked again: %d requests", len(appr.requests()))
	}
	if _, err := m.Connect(t.Context(), "cdp", "work", envFor(t, "s2")); err != nil {
		t.Fatalf("connect work/s2: %v", err)
	}
	if len(appr.requests()) != 2 {
		t.Fatalf("another session reused the grant: %d requests", len(appr.requests()))
	}

	appr.answer = host.ApprovalDeny
	if _, err := m.Connect(t.Context(), "cdp", "personal", envFor(t, "s1")); err == nil {
		t.Fatal("an allow_session for work authorised personal")
	}
	if len(appr.requests()) != 3 {
		t.Fatalf("personal profile did not ask: %d requests", len(appr.requests()))
	}
}

// TestF6EXEC041BrowserExtension (EXEC-041): extension control is off by
// default, fails closed without a manifest, and never widens real_profile.
func TestF6EXEC041BrowserExtension(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"off by default", extensionOff},
		{"missing manifest fails closed", extensionMissingManifest},
		{"enable and disable with a valid manifest", extensionToggle},
		{"negative control: extension does not open the real profile", extensionNoProfile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

func extensionOff(t *testing.T) {
	t.Helper()
	cfg := surfaceConfig(t)
	m := newModule(t.Context(), t, &cfg, Deps{})
	status, err := m.Extension("enable")
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" || status.Extension {
		t.Fatalf("enable = %+v, %v, want denied and off", status, err)
	}
}

func extensionMissingManifest(t *testing.T) {
	t.Helper()
	cfg := surfaceConfig(t)
	cfg.ExtensionControl = true
	m := newModule(t.Context(), t, &cfg, Deps{})
	status, err := m.Extension("enable")
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" || status.Extension {
		t.Fatalf("enable = %+v, %v, want module_unavailable and off", status, err)
	}
	if status.ExtensionReady {
		t.Fatal("status claims a manifest that does not exist")
	}
}

func extensionToggle(t *testing.T) {
	t.Helper()
	cfg := surfaceConfig(t)
	cfg.ExtensionControl = true
	cfg.ExtensionManifest = writeManifest(t, "manifest.json", `{"name":"damnbot-extension","version":1}`)
	m := newModule(t.Context(), t, &cfg, Deps{})
	status, err := m.Extension("enable")
	if err != nil || !status.Extension || !status.ExtensionReady {
		t.Fatalf("enable = %+v, %v", status, err)
	}
	status, err = m.Extension("disable")
	if err != nil || status.Extension {
		t.Fatalf("disable = %+v, %v", status, err)
	}
}

func extensionNoProfile(t *testing.T) {
	t.Helper()
	f := newFake()
	cfg := surfaceConfig(t)
	cfg.RealProfile = false
	cfg.ExtensionControl = true
	cfg.ExtensionManifest = writeManifest(t, "manifest.json", `{"name":"damnbot-extension","version":1}`)
	appr := &fakeApprovals{answer: host.ApprovalAllowSession}
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	if _, err := m.Extension("enable"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := m.Connect(t.Context(), "cdp", "default", envFor(t, "s1")); err == nil {
		t.Fatal("enabling the extension opened the real profile")
	}
	if len(appr.requests()) != 0 || len(f.calls) != 0 {
		t.Fatalf("approvals = %d, CDP calls = %d, want 0", len(appr.requests()), len(f.calls))
	}
}

// TestF6CLI006BrowserCloseProfile (CLI-006): only the named profile is closed,
// a missing lock is idempotent, and an unknown or foreign lock does nothing.
func TestF6CLI006BrowserCloseProfile(t *testing.T) {
	t.Parallel()
	profiles := []Profile{{Name: "chrome", Dir: "/p/chrome"}, {Name: "brave", Dir: "/p/brave"}}
	if _, xerr := CloseProfile("edge", profiles, ProfileHooks{}); !errors.Is(xerr, ErrInvalidProfile) {
		t.Fatalf("unknown profile = %v, want ErrInvalidProfile", xerr)
	}
	closeMissingLock(t, profiles)
	closeForeignLock(t, profiles)
	closeOwnedProfile(t, profiles)
}

func closeMissingLock(t *testing.T, profiles []Profile) {
	t.Helper()
	var touched []string
	hooks := ProfileHooks{
		LockPID: func(dir string) (int, error) { touched = append(touched, dir); return 0, os.ErrNotExist },
		Owns:    func(int, string) (bool, error) { t.Fatal("Owns called without a lock"); return false, nil },
		Stop:    func(int) error { t.Fatal("Stop called without a lock"); return nil },
	}
	res, err := CloseProfile("chrome", profiles, hooks)
	if err != nil || !res.AlreadyClosed || !slices.Equal(touched, []string{"/p/chrome"}) {
		t.Fatalf("missing lock = %+v, %v, touched %v", res, err, touched)
	}
}

func foreignHooks(t *testing.T, touched *[]string, stopped *[]int) ProfileHooks {
	t.Helper()
	return ProfileHooks{
		LockPID: func(dir string) (int, error) { *touched = append(*touched, dir); return 4242, nil },
		Owns: func(pid int, dir string) (bool, error) {
			if pid != 4242 || dir != "/p/chrome" {
				t.Fatalf("owns(%d, %q)", pid, dir)
			}
			return false, nil
		},
		Stop: func(pid int) error { *stopped = append(*stopped, pid); return nil },
	}
}

func closeForeignLock(t *testing.T, profiles []Profile) {
	t.Helper()
	var touched []string
	var stopped []int
	hooks := foreignHooks(t, &touched, &stopped)
	if _, err := CloseProfile("chrome", profiles, hooks); !errors.Is(err, errDenied) {
		t.Fatalf("foreign lock = %v, want errDenied", err)
	}
	if len(stopped) != 0 || !slices.Equal(touched, []string{"/p/chrome"}) {
		t.Fatalf("foreign lock stopped %v, touched %v", stopped, touched)
	}
}

func closeOwnedProfile(t *testing.T, profiles []Profile) {
	t.Helper()
	var touched []string
	var stopped []int
	hooks := foreignHooks(t, &touched, &stopped)
	hooks.Owns = func(int, string) (bool, error) { return true, nil }
	res, err := CloseProfile("chrome", profiles, hooks)
	if err != nil || !res.Closed || res.PID != 4242 || !slices.Equal(stopped, []int{4242}) || !slices.Equal(touched, []string{"/p/chrome"}) {
		t.Fatalf("close = %+v, %v, stopped %v, touched %v", res, err, stopped, touched)
	}
	stopped = nil
	hooks.Stop = func(pid int) error { stopped = append(stopped, pid); return os.ErrProcessDone }
	res, err = CloseProfile("chrome", profiles, hooks)
	if err != nil || !res.AlreadyClosed {
		t.Fatalf("process done = %+v, %v", res, err)
	}
}

// TestControlStatusNeverPrintsEndpoint is a small property of the surface: the
// status line carries the engine name, not the token-bearing endpoint.
func TestControlStatusNeverPrintsEndpoint(t *testing.T) {
	t.Parallel()
	cfg := surfaceConfig(t)
	m := newModule(t.Context(), t, &cfg, Deps{})
	line := m.ControlStatus(host.Env{}).String()
	if strings.Contains(line, "127.0.0.1") || strings.Contains(line, "ws://") {
		t.Fatalf("status leaked the endpoint: %q", line)
	}
}

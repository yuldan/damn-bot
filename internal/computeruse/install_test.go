package computeruse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// TestInstallIdempotentChecksum covers the install contract: the copy lands
// under the data dir, a second pass writes nothing, and a wrong checksum
// leaves neither a target nor a temp file.
func TestInstallIdempotentChecksum(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "cua-driver")
	if err := os.WriteFile(src, []byte("driver-v1"), 0o700); err != nil { // #nosec G306 -- fixture must look executable
		t.Fatal(err)
	}
	sum, err := hashFile(src)
	if err != nil {
		t.Fatal(err)
	}
	opts := InstallOptions{Dir: dir, Source: src, SHA256: sum}
	res, err := Install(opts)
	if err != nil || !res.Installed || !res.Verified {
		t.Fatalf("install = %+v, %v", res, err)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil || string(got) != "driver-v1" {
		t.Fatalf("installed bytes = %q, %v", got, err)
	}
	again, err := Install(opts)
	if err != nil || again.Installed || !again.Verified || again.SHA256 != sum {
		t.Fatalf("second install = %+v, %v", again, err)
	}
	badDir := t.TempDir()
	if _, ierr := Install(InstallOptions{Dir: badDir, Source: src, SHA256: "00"}); !errors.Is(ierr, ErrInvalid) {
		t.Fatalf("short checksum = %v, want ErrInvalid", ierr)
	}
	wrong := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err = Install(InstallOptions{Dir: badDir, Source: src, SHA256: wrong})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("wrong checksum = %v, want ErrChecksum", err)
	}
	assertNoFiles(t, filepath.Join(badDir, "cua"))
	if _, err := Install(InstallOptions{Dir: t.TempDir()}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty install = %v, want ErrUnavailable", err)
	}
}

func assertNoFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial files left in %s: %v", dir, entries)
	}
}

// TestPermissionsReadOnly proves the probe never spawns off macOS and never
// builds a grant argv: a Linux host reports "no TCC model" with zero backend
// calls, and the only macOS argv is `permissions status --json`.
func TestPermissionsReadOnly(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{}
	m := testModule(t, backend, &fakeApprovals{}, 1024)
	assertPermissionsStatus(t, m)
	if backend.count() != 0 {
		t.Fatalf("fake backend was touched %d times", backend.count())
	}
	m.enabled = false
	if _, err := m.Permissions(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled permissions = %v", err)
	}
}

func assertDarwinPermissionProbe(t *testing.T, m *Module) {
	t.Helper()
	// A fake Backend has no permission probe: macOS must fail closed rather
	// than pretend the TCC state is known.
	if _, err := m.Permissions(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fake backend on macOS = %v, want ErrUnavailable", err)
	}
	pm, args := processPermissions(t, `{"accessibility":true,"screen_recording":true}`)
	st, err := pm.Permissions(t.Context())
	if err != nil || !st.Ready || st.Accessibility == nil || !*st.Accessibility ||
		st.ScreenRecording == nil || !*st.ScreenRecording {
		t.Fatalf("process permissions = %+v, %v", st, err)
	}
	assertPermissionArgv(t, *args)
	// A malformed driver reply is unavailable, not a guessed "granted".
	pm, args = processPermissions(t, `{`)
	if _, err := pm.Permissions(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("malformed permissions = %v, want ErrUnavailable", err)
	}
	assertPermissionArgv(t, *args)
}

func processPermissions(t *testing.T, payload string) (*Module, *[]string) {
	t.Helper()
	args := new([]string)
	spawn := func(_ context.Context, spec host.SpawnSpec) (host.Process, error) {
		*args = append([]string(nil), spec.Args...)
		return newStaticProcess([]byte(payload)), nil
	}
	return &Module{
		cfg:     Config{Timeout: time.Second, MaxScreenshotBytes: 1024},
		deps:    Deps{Approvals: &fakeApprovals{}, Secrets: host.DenySecrets{}},
		backend: &processBackend{path: "/bin/fake", spawn: spawn, max: 1024},
		enabled: true, status: host.StatusStopped,
	}, args
}

func assertPermissionArgv(t *testing.T, args []string) {
	t.Helper()
	if len(args) != 3 || args[0] != "permissions" || args[1] != "status" || args[2] != "--json" {
		t.Fatalf("permissions argv = %v, want only the read-only status probe", args)
	}
}

func assertPermissionsStatus(t *testing.T, m *Module) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		assertDarwinPermissionProbe(t, m)
		return
	}
	st, err := m.Permissions(t.Context())
	if err != nil {
		t.Fatalf("permissions: %v", err)
	}
	if !st.Ready || st.Supported {
		t.Fatalf("status = %+v", st)
	}
}

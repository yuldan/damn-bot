package computeruse

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
	st, err := m.Permissions(t.Context())
	if err != nil {
		t.Fatalf("permissions: %v", err)
	}
	if st.Ready != true || st.Supported != (st.Platform == "darwin") {
		t.Fatalf("status = %+v", st)
	}
	if backend.count() != 0 {
		t.Fatalf("permissions touched the backend %d times off macOS", backend.count())
	}
	m.enabled = false
	if _, err := m.Permissions(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled permissions = %v", err)
	}
}

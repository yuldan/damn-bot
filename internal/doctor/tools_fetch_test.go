//go:build !minimal

package doctor

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeFetch struct {
	data  []byte
	calls int
	err   error
}

func (f *fakeFetch) Download(_ context.Context, _ *ToolArtifact, dst *os.File) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	_, err := dst.Write(f.data)
	if err != nil {
		return fmt.Errorf("fake fetch: %w", err)
	}
	return nil
}

// TestF6GAPTOOL06DoctorTools pins the full GAP-TOOL-06 surface: fetch
// verifies the checksum before install, is idempotent, refuses without
// license confirmation and never leaves a partial file, and `verify` rejects
// a modified binary. Sub-checks use the production parser and install path.
func TestF6GAPTOOL06DoctorTools(t *testing.T) {
	t.Parallel()
	t.Run("fetch verifies and is idempotent", checkFetchIdempotent)
	t.Run("checksum mismatch leaves no partial file", checkChecksumMismatch)
	t.Run("fetch without license is refused", checkLicenseRefusal)
	t.Run("fetch refuses unknown or unpinned tools", checkFetchRefusals)
	t.Run("verify rejects a modified binary", checkFetchVerify)
	t.Run("zip extraction", checkZipExtraction)
	t.Run("tar.gz extraction", checkTarGzExtraction)
}

func checkFetchIdempotent(t *testing.T) {
	payload := []byte("demo-binary")
	lock := testLock(t, payload)
	root := t.TempDir()
	dl := &fakeFetch{data: payload}
	st, err := FetchTool(context.Background(), lock, &FetchOptions{
		Root: root, Name: "demo", GOOS: "linux", GOARCH: "amd64",
		AcceptLicense: true, Downloader: dl,
	})
	if err != nil || !st.Installed || !st.OK || st.Detail != "installed" || dl.calls != 1 {
		t.Fatalf("fetch = %+v, %v, calls %d", st, err, dl.calls)
	}
	b, rerr := os.ReadFile(st.Path)
	if rerr != nil || !bytes.Equal(b, payload) {
		t.Fatalf("installed = %q, %v", b, rerr)
	}
	fi, serr := os.Stat(st.Path)
	if serr != nil {
		t.Fatalf("stat: %v", serr)
	}
	// Windows has no Unix permission bits: the installer still writes the
	// verified bytes at the pinned path, so only the content/idempotency
	// assertions apply there. Unix keeps the owner-only 0700 contract.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", fi.Mode().Perm())
	}
	again, err := FetchTool(context.Background(), lock, &FetchOptions{
		Root: root, Name: "demo", GOOS: "linux", GOARCH: "amd64",
		AcceptLicense: true, Downloader: &fakeFetch{err: errors.New("must not download")},
	})
	if err != nil || again.Detail != "already installed" {
		t.Fatalf("idempotent fetch = %+v, %v", again, err)
	}
}

func checkChecksumMismatch(t *testing.T) {
	lock := testLock(t, []byte("expected"))
	root := t.TempDir()
	_, err := FetchTool(context.Background(), lock, &FetchOptions{
		Root: root, Name: "demo", GOOS: "linux", GOARCH: "amd64",
		AcceptLicense: true, Downloader: &fakeFetch{data: []byte("tampered")},
	})
	if !errors.Is(err, ErrToolChecksum) {
		t.Fatalf("fetch = %v, want ErrToolChecksum", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tools", "demo", "1.0", "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial binary installed: %v", err)
	}
	if rest, err := os.ReadDir(filepath.Join(root, "tools", ".tmp")); err != nil || len(rest) != 0 {
		t.Fatalf("temp leftovers = %v, %v", rest, err)
	}
}

func checkLicenseRefusal(t *testing.T) {
	lock := testLock(t, []byte("demo-binary"))
	dl := &fakeFetch{data: []byte("demo-binary")}
	_, err := FetchTool(context.Background(), lock, &FetchOptions{
		Root: t.TempDir(), Name: "demo", GOOS: "linux", GOARCH: "amd64", Downloader: dl,
	})
	if !errors.Is(err, ErrToolLicense) || !strings.Contains(err.Error(), "MIT") || dl.calls != 0 {
		t.Fatalf("fetch = %v, calls %d", err, dl.calls)
	}
}

func checkFetchRefusals(t *testing.T) {
	lock := testLock(t, []byte("demo-binary"))
	_, err := FetchTool(context.Background(), lock, &FetchOptions{
		Root: t.TempDir(), Name: "nope", GOOS: "linux", GOARCH: "amd64", AcceptLicense: true,
	})
	if !errors.Is(err, ErrToolUnknown) {
		t.Fatalf("unknown = %v", err)
	}
	_, err = FetchTool(context.Background(), lock, &FetchOptions{
		Root: t.TempDir(), Name: "unpinned", GOOS: "linux", GOARCH: "amd64", AcceptLicense: true,
	})
	if !errors.Is(err, ErrToolNoArtifact) {
		t.Fatalf("unpinned = %v", err)
	}
}

func checkFetchVerify(t *testing.T) {
	payload := []byte("demo-binary")
	lock := testLock(t, payload)
	root := t.TempDir()
	st, err := FetchTool(context.Background(), lock, &FetchOptions{
		Root: root, Name: "demo", GOOS: "linux", GOARCH: "amd64",
		AcceptLicense: true, Downloader: &fakeFetch{data: payload},
	})
	if err != nil {
		t.Fatal(err)
	}
	list, verr := ToolsVerify(lock, root, "linux", "amd64")
	if verr != nil || !list[0].OK {
		t.Fatalf("clean verify = %+v, %v", list, verr)
	}
	if err := os.WriteFile(st.Path, []byte("demo-binarY"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, verr = ToolsVerify(lock, root, "linux", "amd64")
	if !errors.Is(verr, ErrToolVerify) || list[0].OK || !strings.Contains(list[0].Detail, "checksum mismatch") {
		t.Fatalf("tampered verify = %+v, %v", list, verr)
	}
	for _, row := range list {
		if strings.Contains(row.Detail, "http") || strings.Contains(row.Path, "http") {
			t.Fatalf("verify printed a URL: %+v", row)
		}
	}
}

func checkZipExtraction(t *testing.T) {
	member := "demo-1.0/bin/demo"
	payload := []byte("from-archive")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	st := fetchArchive(t, buf.Bytes(), "zip", member, payload)
	if b, err := os.ReadFile(st.Path); err != nil || !bytes.Equal(b, payload) {
		t.Fatalf("extracted = %q, %v", b, err)
	}
}

func checkTarGzExtraction(t *testing.T) {
	member := "demo-1.0/bin/demo"
	payload := []byte("from-targz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0o700, Size: int64(len(payload)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	st := fetchArchive(t, buf.Bytes(), "tar.gz", member, payload)
	if b, err := os.ReadFile(st.Path); err != nil || !bytes.Equal(b, payload) {
		t.Fatalf("extracted = %q, %v", b, err)
	}
}

// archiveLock pins one archive member through the production parser.
func archiveLock(t *testing.T, archive []byte, kind, member string, payload []byte) *ToolsLock {
	t.Helper()
	doc := fmt.Sprintf(`{"schema":%q,"tools":[{"name":"demo","version":"1.0",`+
		`"license":"MIT","license_url":"https://example.invalid/l","artifacts":[{`+
		`"os":"linux","arch":"amd64","url":"https://example.invalid/x",`+
		`"sha256":%q,"binary_sha256":%q,"size":%d,"archive":%q,"binary":%q}]}]}`,
		ToolsSchema, sha(archive), sha(payload), len(archive), kind, member)
	lock, err := ParseToolsLock([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return lock
}

func fetchArchive(t *testing.T, archive []byte, kind, member string, payload []byte) ToolStatus {
	t.Helper()
	lock := archiveLock(t, archive, kind, member, payload)
	st, err := FetchTool(context.Background(), lock, &FetchOptions{
		Root: t.TempDir(), Name: "demo", GOOS: "linux", GOARCH: "amd64",
		AcceptLicense: true, Downloader: &fakeFetch{data: archive},
	})
	if err != nil || !st.Installed {
		t.Fatalf("%s fetch = %+v, %v", kind, st, err)
	}
	return st
}

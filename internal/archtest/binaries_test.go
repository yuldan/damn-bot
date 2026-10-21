package archtest

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// maxTrackedBytes is the size above which a tracked file needs an
// allowlist entry (REVIEW-F2-SEC5-8: build outputs were committed three
// times).
const maxTrackedBytes = 1 << 20

// largeFileAllow lists the tracked files allowed above maxTrackedBytes, with
// their reason.
func largeFileAllow() map[string]string {
	return map[string]string{ //nolint:gosec // G101: repository paths, not credentials
		"bench/budget/o200k/o200k_base.tiktoken.gz": "offline o200k_base vocabulary of the P1 token harness (F0 deliverable 1)",
	}
}

// TestNoLargeOrBinaryFiles fails on any tracked file larger than 1 MiB
// outside largeFileAllow and on any tracked native executable (ELF,
// Mach-O, PE): binaries are build outputs, never sources.
func TestNoLargeOrBinaryFiles(t *testing.T) {
	t.Parallel()
	allow := largeFileAllow()
	out := runRepo(t, exec.CommandContext(t.Context(), "git", "ls-files", "-z"))
	for p := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if p == "" {
			continue
		}
		fi, err := os.Lstat(filepath.Join(repoDir(), filepath.FromSlash(p)))
		if err != nil || !fi.Mode().IsRegular() {
			continue // deleted in the worktree, or a symlink/submodule
		}
		if _, ok := allow[p]; !ok && fi.Size() > maxTrackedBytes {
			t.Errorf("%s: %d bytes > %d; build outputs must not be committed (add a reasoned largeFileAllow entry only for source data)", p, fi.Size(), maxTrackedBytes)
		}
		if kind := executableKind(t, p); kind != "" {
			t.Errorf("%s: tracked %s executable; remove it and ignore the build output", p, kind)
		}
	}
	for p := range allow {
		if _, err := os.Stat(filepath.Join(repoDir(), filepath.FromSlash(p))); err != nil {
			t.Errorf("largeFileAllow entry %s is stale: %v", p, err)
		}
	}
}

// executableKind sniffs the native executable formats from the header.
func executableKind(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(repoDir(), filepath.FromSlash(p)))
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close %s: %v", p, cerr)
		}
	}()
	head := make([]byte, 64)
	n, err := io.ReadFull(f, head)
	if err != nil && n == 0 {
		return ""
	}
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("\x7fELF")):
		return "ELF"
	case len(head) >= 4 && isMachO(binary.BigEndian.Uint32(head)):
		return "Mach-O"
	case len(head) >= 64 && bytes.HasPrefix(head, []byte("MZ")):
		var pe [4]byte
		if _, rerr := f.ReadAt(pe[:], int64(binary.LittleEndian.Uint32(head[0x3c:]))); rerr == nil && string(pe[:]) == "PE\x00\x00" {
			return "PE"
		}
	}
	return ""
}

// isMachO matches the thin 32/64-bit Mach-O magics in both byte orders.
func isMachO(magic uint32) bool {
	switch magic {
	case 0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe:
		return true
	}
	return false
}

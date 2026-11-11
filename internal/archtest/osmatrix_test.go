package archtest

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// f9OSTargets = 8 target rilis kanonik (ENGINEERING E-39, CROSS-CUTTING §7).
func f9OSTargets() []string {
	return []string{
		"linux/amd64", "linux/arm64", "linux/arm",
		"darwin/amd64", "darwin/arm64",
		"windows/amd64", "windows/arm64",
		"android/arm64",
	}
}

var osArchRe = regexp.MustCompile(`^(amd64|arm64|arm)\b`)

// TestF9OSMatrix (ROADMAP F9 deliverable, CROSS-CUTTING §7): matriks OS F9
// mencakup 8 target rilis kanonik dan runner CI penuh. Bagian yang menuntut
// QEMU (linux/arm, android/arm64) belum berjalan selama F9, jadi kegagalan di
// sana ditunda sampai penanda rilis (ADR-347).
func TestF9OSMatrix(t *testing.T) {
	t.Parallel()
	targets := makefileTargets(t)
	want := f9OSTargets()
	if !slices.Equal(targets, want) {
		t.Fatalf("TARGETS = %v, want %v (E-39)", targets, want)
	}
	docs := crossCuttingTargets(t)
	wantSorted := slices.Clone(want)
	slices.Sort(docs)
	slices.Sort(wantSorted)
	if !slices.Equal(docs, wantSorted) {
		t.Fatalf("CROSS-CUTTING §7 = %v, want %v", docs, wantSorted)
	}
	ci := readRepoFile(t, ".github/workflows/ci.yml")
	for _, target := range want {
		if !strings.Contains(ci, `"target":"`+target+`"`) {
			t.Errorf("job sqlite-engine tidak memuat target %s", target)
		}
	}
	for _, os := range []string{"ubuntu-latest", "ubuntu-24.04-arm", "macos-latest", "macos-15-intel", "windows-latest", "windows-11-arm"} {
		if !strings.Contains(ci, `"os":"`+os+`"`) {
			t.Errorf("matrix test penuh tidak memuat runner %s", os)
		}
	}
	var qemu []string
	for _, target := range []string{"linux/arm", "android/arm64"} {
		if !strings.Contains(ci, `"target":"`+target+`","mode":"run"`) {
			qemu = append(qemu, target)
		}
	}
	if len(qemu) > 0 {
		skipOrFail(t, "target QEMU belum berjalan: "+strings.Join(qemu, ", "))
	}
}

// makefileTargets membaca daftar `TARGETS := …` (E-39).
func makefileTargets(t *testing.T) []string {
	t.Helper()
	for line := range strings.Lines(readRepoFile(t, "Makefile")) {
		rest, ok := strings.CutPrefix(line, "TARGETS := ")
		if ok {
			return strings.Fields(rest)
		}
	}
	t.Fatal("Makefile: TARGETS tidak ditemukan")
	return nil
}

// crossCuttingTargets menyusun goos/arch dari tabel CROSS-CUTTING §7.
func crossCuttingTargets(t *testing.T) []string {
	t.Helper()
	var out []string
	section := sectionText(t, readRepoFile(t, "docs/CROSS-CUTTING.md"), "## 7. Lintas platform", "\n## ")
	for line := range strings.Lines(section) {
		cells := tableCells(line)
		if len(cells) < 2 {
			continue
		}
		fields := strings.Fields(cells[0])
		if len(fields) == 0 {
			continue
		}
		goos := map[string]string{
			"Linux": "linux", "macOS": "darwin", "Windows": "windows", "Android": "android",
		}[fields[0]]
		if goos == "" {
			continue
		}
		for arch := range strings.SplitSeq(cells[1], ",") {
			arch = strings.TrimSpace(arch)
			m := osArchRe.FindStringSubmatch(arch)
			if m == nil {
				continue
			}
			out = append(out, goos+"/"+m[1])
		}
	}
	return out
}

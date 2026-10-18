package archtest

import (
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	ctIDRe    = regexp.MustCompile(`^CT-\d{2}$`)
	ctTestRe  = regexp.MustCompile("`((?:Test|Benchmark)[A-Za-z0-9_]+)(?:/[^`]*)?`")
	ctOtherRe = regexp.MustCompile("`(make [a-z-]+|cd bench && go run \\./[a-z/]+)`")
	goTestRe  = regexp.MustCompile(`(?m)^func ((?:Test|Benchmark)[A-Za-z0-9_]+)\(|b\.Run\("([A-Za-z0-9_]+)"`)
)

// ctRows mem-parse baris ID CT dari tabel bagian heading (sampai heading `## ` berikutnya):
// ID → sel verifikasi (kolom terakhir).
func ctRows(t *testing.T, text, heading string) map[string]string {
	t.Helper()
	_, sec, ok := strings.Cut(text, "\n"+heading)
	if !ok {
		t.Fatalf("heading %q tidak ada di CODE-TOOLS.md", heading)
	}
	sec, _, _ = strings.Cut(sec, "\n## ")
	rows := map[string]string{}
	for line := range strings.Lines(sec) {
		cells := tableCells(line)
		if len(cells) < 2 || !ctIDRe.MatchString(cells[0]) {
			continue
		}
		if _, dup := rows[cells[0]]; dup {
			t.Errorf("%s: %s ganda", heading, cells[0])
		}
		rows[cells[0]] = cells[len(cells)-1]
	}
	return rows
}

// repoTestNames mengumpulkan nama Test*/Benchmark* (fungsi top-level dan sub-benchmark
// b.Run) dari semua *_test.go di repo, termasuk modul bench/ dan tools/.
func repoTestNames(t *testing.T) map[string]bool {
	t.Helper()
	fsys := openRepo(t).FS()
	names := map[string]bool{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && p != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "vendor"):
			return fs.SkipDir
		case d.IsDir() || !strings.HasSuffix(p, "_test.go"):
			return nil
		}
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		for _, m := range goTestRe.FindAllStringSubmatch(string(src), -1) {
			if m[1] != "" {
				names[m[1]] = true
			} else {
				names["Benchmark"+m[2]] = true // sub-benchmark: BenchmarkCodetools/GrepToolC3 → BenchmarkGrepToolC3
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// TestCodeToolsIDsComplete adalah gate dokumen P23 (CODE-TOOLS §11, ADR-063 m-15, ROADMAP
// F1): tabel ringkasan memuat tepat 31 ID CT unik (CT-01..14, CT-20..29, CT-30..36), setiap
// baris menyebut verifikasi (test/benchmark, target make, atau driver bench), setiap baris
// §10.2/§10.3 ada di ringkasan, dan setiap test/benchmark yang disebut untuk ID F1 ada di
// sumber repo (ID F5 dikecualikan).
func TestCodeToolsIDsComplete(t *testing.T) {
	t.Parallel()
	doc := readRepoFile(t, "docs/CODE-TOOLS.md")
	summary := ctRows(t, doc, "## 11. Ringkasan angka")
	var want []string
	for _, r := range [][2]int{{1, 14}, {20, 29}, {30, 36}} {
		for n := r[0]; n <= r[1]; n++ {
			want = append(want, fmt.Sprintf("CT-%02d", n))
		}
	}
	got := slices.Sorted(maps.Keys(summary))
	if !slices.Equal(got, want) {
		t.Fatalf("ID CT di §11 = %v (%d), want 31 ID %v", got, len(got), want)
	}
	for id, row := range ctRows(t, doc, "## 10. Benchmark dan suite akurasi") {
		if _, ok := summary[id]; !ok {
			t.Errorf("%s ada di §10 tetapi tidak di ringkasan §11", id)
		}
		if !ctTestRe.MatchString(row) && !ctOtherRe.MatchString(row) {
			t.Errorf("%s (§10): kolom verifikasi tanpa test/benchmark/driver: %q", id, row)
		}
	}
	ctCheckTestsExist(t, summary, want)
}

// ctCheckTestsExist: setiap test/benchmark yang disebut baris ID F1 ada di sumber repo.
// ID CT yang butuh codeintel/codeindex (F5) dikecualikan (CODE-TOOLS §11 P23, ROADMAP F1),
// begitu pula nama test F5 di baris ID F1 (CT-34 bagian TestIndexActivation).
func ctCheckTestsExist(t *testing.T, summary map[string]string, ids []string) {
	t.Helper()
	f5IDs := []string{"CT-08", "CT-12", "CT-13", "CT-24", "CT-28"}
	f5Tests := []string{"TestIndexActivation"}
	names := repoTestNames(t)
	for _, id := range ids {
		row := summary[id]
		tests := ctTestRe.FindAllStringSubmatch(row, -1)
		if len(tests) == 0 && !ctOtherRe.MatchString(row) {
			t.Errorf("%s: kolom verifikasi tanpa test/benchmark/driver: %q", id, row)
		}
		if slices.Contains(f5IDs, id) {
			continue
		}
		for _, m := range tests {
			if !names[m[1]] && !slices.Contains(f5Tests, m[1]) {
				t.Errorf("%s: %s tidak ada di *_test.go repo", id, m[1])
			}
		}
	}
}

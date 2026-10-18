package archtest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// originFile = sidecar asal per direktori (ADR-018 butir b, ADR-099): file turunan pihak ketiga
// berformat data (JSON, gz, tsv, ids) tidak bisa memuat header, jadi asalnya dicatat di sini.
const originFile = "ORIGIN.md"

// originFields = baris wajib setiap bagian `## <sumber>` di ORIGIN.md.
func originFields() []string { return []string{"Sumber", "Commit", "Lisensi"} }

// attributionRow = satu baris tabel docs/ATTRIBUTION.md: glob path file turunan + file notice.
type attributionRow struct{ glob, notice string }

// attributionRows mem-parse tabel docs/ATTRIBUTION.md (kolom 1 = glob dalam backtick, kolom 4 =
// path notice dalam backtick).
func attributionRows(text string) ([]attributionRow, []string) {
	var rows []attributionRow
	var errs []string
	for raw := range strings.Lines(text) {
		cells := tableCells(raw)
		if len(cells) != 4 || cells[0] == "File" || strings.HasPrefix(cells[0], "---") {
			continue
		}
		g, n := strings.Trim(cells[0], "`"), strings.Trim(cells[3], "`")
		if g == cells[0] || n == cells[3] {
			errs = append(errs, fmt.Sprintf("ATTRIBUTION.md: baris tanpa path backtick: %q", strings.TrimSpace(raw)))
			continue
		}
		rows = append(rows, attributionRow{glob: g, notice: n})
	}
	if len(rows) == 0 {
		errs = append(errs, "ATTRIBUTION.md: tabel kosong")
	}
	return rows, errs
}

// globMatch mencocokkan path slash dengan glob ATTRIBUTION: `**` = nol atau lebih segmen,
// segmen lain memakai path.Match.
func globMatch(glob, name string) bool {
	return matchSegs(strings.Split(glob, "/"), strings.Split(name, "/"))
}

func matchSegs(g, n []string) bool {
	if len(g) == 0 {
		return len(n) == 0
	}
	if g[0] == "**" {
		for i := 0; i <= len(n); i++ {
			if matchSegs(g[1:], n[i:]) {
				return true
			}
		}
		return false
	}
	if len(n) == 0 {
		return false
	}
	ok, err := path.Match(g[0], n[0])
	return err == nil && ok && matchSegs(g[1:], n[1:])
}

// originEntry = satu baris tabel file di ORIGIN.md beserta bagian sumbernya.
type originEntry struct{ section, sha string }

// parseOrigin mem-parse ORIGIN.md: bagian `## <nama>` dengan baris `- <Field>: <nilai>` untuk
// setiap originFields, lalu baris tabel "| `file` | <sha256 hex> |".
func parseOrigin(dir, text string) (map[string]originEntry, []string) {
	entries := map[string]originEntry{}
	var errs []string
	section := ""
	fields := map[string]map[string]bool{}
	for raw := range strings.Lines(text) {
		line := strings.TrimRight(raw, "\r\n")
		if name, ok := strings.CutPrefix(line, "## "); ok {
			section = strings.TrimSpace(name)
			fields[section] = map[string]bool{}
			continue
		}
		if f, ok := strings.CutPrefix(line, "- "); ok && section != "" {
			if k, v, ok := strings.Cut(f, ":"); ok && strings.TrimSpace(v) != "" {
				fields[section][k] = true
			}
			continue
		}
		if err := addOriginRow(entries, section, tableCells(line)); err != "" {
			errs = append(errs, fmt.Sprintf("%s/%s: %s", dir, originFile, err))
		}
	}
	for s, got := range fields {
		for _, f := range originFields() {
			if !got[f] {
				errs = append(errs, fmt.Sprintf("%s/%s: bagian %q tanpa baris \"- %s: …\"", dir, originFile, s, f))
			}
		}
	}
	return entries, errs
}

// addOriginRow mencatat satu baris tabel file ORIGIN.md; pesan kosong = sah atau bukan baris file.
func addOriginRow(entries map[string]originEntry, section string, cells []string) string {
	if len(cells) != 2 || cells[0] == "File" || strings.HasPrefix(cells[0], "---") {
		return ""
	}
	name := strings.Trim(cells[0], "`")
	switch {
	case section == "":
		return name + " di luar bagian ## sumber"
	case name == cells[0] || strings.Contains(name, "/"):
		return fmt.Sprintf("nama file %q harus basename dalam backtick", cells[0])
	case len(cells[1]) != 2*sha256.Size:
		return "SHA-256 " + name + " tidak valid"
	case entries[name] != (originEntry{}):
		return name + " tercantum dua kali"
	}
	entries[name] = originEntry{section: section, sha: cells[1]}
	return ""
}

// derivedFiles mengembalikan file (tanpa ORIGIN.md, tanpa direktori tersembunyi) yang cocok
// dengan glob ATTRIBUTION, dikelompokkan per direktori, beserta glob yang tidak cocok apa pun.
func derivedFiles(fsys fs.FS, rows []attributionRow) (map[string][]string, []string, error) {
	byDir := map[string][]string{}
	used := make([]bool, len(rows))
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && p != "." && strings.HasPrefix(d.Name(), "."):
			return fs.SkipDir
		case d.IsDir() || d.Name() == originFile:
			return nil
		}
		for i, r := range rows {
			if globMatch(r.glob, p) {
				used[i] = true
				byDir[path.Dir(p)] = append(byDir[path.Dir(p)], path.Base(p))
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk: %w", err)
	}
	var dead []string
	for i, r := range rows {
		if !used[i] {
			dead = append(dead, r.glob)
		}
	}
	return byDir, dead, nil
}

// checkAttribution mengembalikan semua pelanggaran ADR-018 (b)/(c) + ADR-099 di fsys.
func checkAttribution(fsys fs.FS, attribution string) []string {
	rows, errs := attributionRows(attribution)
	for _, r := range rows {
		if _, err := fs.Stat(fsys, r.notice); err != nil {
			errs = append(errs, fmt.Sprintf("ATTRIBUTION.md: notice %s tidak ada", r.notice))
		}
	}
	byDir, dead, err := derivedFiles(fsys, rows)
	if err != nil {
		return append(errs, err.Error())
	}
	for _, g := range dead {
		errs = append(errs, fmt.Sprintf("ATTRIBUTION.md: glob %s tidak cocok dengan file mana pun", g))
	}
	for dir, files := range byDir {
		errs = append(errs, checkOriginDir(fsys, dir, files)...)
	}
	slices.Sort(errs)
	return errs
}

// checkOriginDir: setiap file turunan di dir tercantum di dir/ORIGIN.md dengan SHA-256 byte-persis
// (bukan dinormalisasi; .gitattributes memasang -text), dan setiap baris ORIGIN.md menunjuk file
// turunan yang ada.
func checkOriginDir(fsys fs.FS, dir string, files []string) []string {
	text, err := fs.ReadFile(fsys, path.Join(dir, originFile))
	if err != nil {
		return []string{fmt.Sprintf("%s: %s tidak ada (ADR-099)", dir, originFile)}
	}
	entries, errs := parseOrigin(dir, string(text))
	for _, f := range files {
		e, ok := entries[f]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s/%s: tidak tercantum di %s", dir, f, originFile))
			continue
		}
		delete(entries, f)
		b, rerr := fs.ReadFile(fsys, path.Join(dir, f))
		if rerr != nil {
			errs = append(errs, rerr.Error())
			continue
		}
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != e.sha {
			errs = append(errs, fmt.Sprintf("%s/%s: SHA-256 %x ≠ %s di %s", dir, f, sum, e.sha, originFile))
		}
	}
	for f := range entries {
		errs = append(errs, fmt.Sprintf("%s/%s: tercantum di %s tetapi tidak ada/tidak cocok glob ATTRIBUTION", dir, f, originFile))
	}
	return errs
}

// TestAttributionHeaders (ADR-018 butir b–c, ADR-099): setiap file yang cocok dengan glob
// docs/ATTRIBUTION.md tercantum di ORIGIN.md direktorinya (sumber, commit, lisensi, SHA-256
// byte-persis), tanpa entri basi, glob mati, atau notice yang hilang.
func TestAttributionHeaders(t *testing.T) {
	t.Parallel()
	for _, e := range checkAttribution(openRepo(t).FS(), readRepoFile(t, "docs/ATTRIBUTION.md")) {
		t.Error(e)
	}
}

// TestAttributionChecker: setiap kelas pelanggaran terdeteksi (meta-test checker di atas).
func TestAttributionChecker(t *testing.T) {
	t.Parallel()
	const att = "| File | Asal | Lisensi | Notice |\n|---|---|---|---|\n" +
		"| `d/**` | x | MIT | `n.txt` |\n"
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	origin := func(rows string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("## x\n- Sumber: u\n- Commit: c\n- Lisensi: MIT\n\n| File | SHA-256 |\n|---|---|\n" + rows)}
	}
	okRow := "| `a.json` | " + sum("A") + " |\n"
	cases := []struct {
		name, att, want string
		fs              fstest.MapFS
	}{
		{name: "ok", att: att, fs: fstest.MapFS{"n.txt": {}, "d/a.json": {Data: []byte("A")}, "d/ORIGIN.md": origin(okRow)}},
		{name: "no origin", att: att, want: "ORIGIN.md tidak ada", fs: fstest.MapFS{"n.txt": {}, "d/a.json": {Data: []byte("A")}}},
		{name: "sha", att: att, want: "SHA-256", fs: fstest.MapFS{"n.txt": {}, "d/a.json": {Data: []byte("B")}, "d/ORIGIN.md": origin(okRow)}},
		{name: "unlisted", att: att, want: "tidak tercantum", fs: fstest.MapFS{"n.txt": {}, "d/a.json": {Data: []byte("A")}, "d/s/b.gz": {}, "d/s/ORIGIN.md": origin("")}},
		{name: "stale", att: att, want: "tidak ada/tidak cocok", fs: fstest.MapFS{"n.txt": {}, "d/a.json": {Data: []byte("A")}, "d/ORIGIN.md": origin(okRow + "| `c.tsv` | " + sum("") + " |\n")}},
		{name: "field", att: att, want: "Commit", fs: fstest.MapFS{"n.txt": {}, "d/a.json": {Data: []byte("A")}, "d/ORIGIN.md": {Data: []byte("## x\n- Sumber: u\n- Lisensi: MIT\n" + okRow)}}},
		{name: "dead glob", att: att, want: "tidak cocok dengan file", fs: fstest.MapFS{"n.txt": {}}},
		{name: "notice", att: att, want: "notice n.txt", fs: fstest.MapFS{"d/a.json": {Data: []byte("A")}, "d/ORIGIN.md": origin(okRow)}},
	}
	for _, c := range cases {
		got := strings.Join(checkAttribution(c.fs, c.att), "\n")
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: pelanggaran %q, ingin memuat %q", c.name, got, c.want)
		}
	}
}

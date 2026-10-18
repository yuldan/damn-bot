package codetools

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases (EDGE-CASES-hermes.md, ADR-077/080): EC-FIO entries owned
// by the file tools.
//
//nolint:gocognit,gocyclo // one closure per catalog entry, like netx/retry
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(t *testing.T)
	}{
		// Overwriting a file never read (or changed since) is refused.
		{"EC-FIO-001", func(t *testing.T) {
			v := newEnv(t)
			v.put(t, "f.txt", "mine\n")
			_, err := v.e.Write(context.Background(), WriteArgs{Path: "f.txt", Content: "clobber\n"}, Opts{})
			wantCode(t, err, CodeStaleRead)
			if v.get(t, "f.txt") != "mine\n" {
				t.Error("file overwritten")
			}
		}},
		// read_file has a line cap and a char cap per page.
		{"EC-FIO-004", func(t *testing.T) {
			v := newEnv(t)
			v.put(t, "f.txt", strings.Repeat(strings.Repeat("w", 1500)+"\n", 3000))
			r := v.read(t, ReadArgs{Path: "f.txt", Limit: readMaxLimit})
			if n := len([]rune(r.Content)); n > DefaultPageChars || r.Meta["next"] == nil {
				t.Errorf("page %d chars", n)
			}
		}},
		// Agent writes never add a BOM.
		{"EC-FIO-008", func(t *testing.T) {
			v := newEnv(t)
			if _, err := v.e.Write(context.Background(), WriteArgs{Path: "n.txt", Content: "é\n"}, Opts{}); err != nil {
				t.Fatal(err)
			}
			if b := v.get(t, "n.txt"); b != "é\n" {
				t.Errorf("written %q", b)
			}
		}},
		// Replacing text next to combining characters never duplicates it.
		{"EC-FIO-011", func(t *testing.T) {
			v := newEnv(t)
			v.put(t, "u.txt", "cafe\u0301 = 1\nnai\u0308ve\n")
			tag := v.tag(t, "u.txt")
			_, err := v.e.Edit(context.Background(), EditArgs{Path: "u.txt", Tag: tag, Edits: []EditItem{{OldString: new("\u0301 = 1"), NewString: new("\u0301 = 2")}}}, Opts{})
			if err != nil || v.get(t, "u.txt") != "cafe\u0301 = 2\nnai\u0308ve\n" {
				t.Errorf("%q %v", v.get(t, "u.txt"), err)
			}
		}},
		// CRLF and LF count lines the same; diffs show logical lines.
		{"EC-FIO-013", func(t *testing.T) {
			v := newEnv(t)
			v.put(t, "w.txt", "a\r\nb\r\nc\r\n")
			r := v.read(t, ReadArgs{Path: "w.txt"})
			if !strings.Contains(r.Content, "L1-3/3 · utf-8 crlf\n1|a\n2|b\n3|c") {
				t.Errorf("%q", r.Content)
			}
		}},
		// File names that are not valid UTF-8 are walked and reported.
		{"EC-FIO-014", func(t *testing.T) {
			if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
				t.Log("APFS/NTFS reject non-UTF-8 names: walking such names is Linux-only")
				return
			}
			v := newEnv(t)
			v.put(t, "bad\xffname.txt", "needle\n")
			r := grep(t, v, &GrepArgs{Pattern: "needle", Mode: "files"})
			if !strings.Contains(r.Content, "bad\xffname.txt #") {
				t.Errorf("%q", r.Content)
			}
		}},
		// Pseudo-files and plain files are read as bytes, never via the locale.
		{"EC-FIO-015", func(t *testing.T) {
			v := newEnv(t)
			p := filepath.Join(v.out, "status")
			if runtime.GOOS == "linux" {
				p = "/proc/self/status"
			} else {
				writeFile(t, p, "Name:\tdamnbot ü\n")
			}
			r, err := v.e.Read(context.Background(), ReadArgs{Path: p}, Opts{Approved: true})
			if err != nil || !strings.Contains(r.Content, "1|Name:\t") || strings.ContainsRune(r.Content, '\uFFFD') {
				t.Errorf("%q %v", r.Content, err)
			}
		}},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

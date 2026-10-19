package codetools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// suiteCase is one CT-20 case of testdata/editsuite/*.txtar.
type suiteCase struct {
	Name        string     `json:"name"`
	Tool        string     `json:"tool"`
	Input       bytesField `json:"input"`
	InputRepeat *repeat    `json:"input_repeat"`
	Edits       []EditItem `json:"edits"`
	Content     string     `json:"content"`
	Tag         bool       `json:"tag"`
	TagOverride string     `json:"tag_override"`
	Mutate      *bytesField
	Retry       bool   `json:"retry"`
	Setup       string `json:"setup"`
	Approved    bool   `json:"approved"`
	Mode        string `json:"mode"`
	Xattr       bool   `json:"xattr"`
	WantCode    Code   `json:"want_code"`
	Want        *bytesField
	WantRepeat  *repeat `json:"want_repeat"`
	WantHint    string  `json:"want_hint"`
	WantMsg     string  `json:"want_msg"`
}

type bytesField struct {
	Text *string `json:"text"`
	Hex  string  `json:"hex"`
}

func (b *bytesField) bytes(t testing.TB) []byte {
	if b.Text != nil {
		return []byte(*b.Text)
	}
	v, err := hex.DecodeString(b.Hex)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	return v
}

// repeat is prefix + unit×count + suffix, generated at test time so the
// suite does not commit megabytes.
type repeat struct {
	Prefix string `json:"prefix"`
	Unit   string `json:"unit"`
	Count  int    `json:"count"`
	Suffix string `json:"suffix"`
}

// writeTo streams the bytes; bufio.Writer errors are sticky, so Flush
// reports any failed write.
func (r *repeat) writeTo(w io.Writer) error {
	bw := bufio.NewWriterSize(w, 1<<20)
	chunk := strings.Repeat(r.Unit, max(1, 4096/max(1, len(r.Unit))))
	per := len(chunk) / max(1, len(r.Unit))
	_, err := bw.WriteString(r.Prefix)
	for n := r.Count; n > 0 && err == nil; n -= per {
		if n < per {
			chunk = strings.Repeat(r.Unit, n)
		}
		_, err = bw.WriteString(chunk)
	}
	if err == nil {
		_, err = bw.WriteString(r.Suffix)
	}
	if err = errors.Join(err, bw.Flush()); err != nil {
		return fmt.Errorf("repeat: %w", err)
	}
	return nil
}

func (r *repeat) sum(t testing.TB) string {
	h := sha256.New()
	if err := r.writeTo(h); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadSuite parses every txtar archive: "-- name --" sections holding one
// JSON case each.
func loadSuite(t *testing.T) []suiteCase {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "editsuite", "*.txtar"))
	if err != nil || len(files) == 0 {
		t.Fatalf("suite files: %v %d", err, len(files))
	}
	var out []suiteCase
	sect := regexp.MustCompile(`(?m)^-- (\S+) --\r?\n`)
	for _, f := range files {
		data := readFixture(t, f)
		locs := sect.FindAllSubmatchIndex(data, -1)
		for i, l := range locs {
			end := len(data)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			var c suiteCase
			if err := json.Unmarshal(data[l[1]:end], &c); err != nil {
				t.Fatalf("%s %s: %v", f, data[l[2]:l[3]], err)
			}
			out = append(out, c)
		}
	}
	return out
}

// TestEditAccuracySuite is CT-20: ≥ 330 cases, 100% expected outcome, the
// file is byte-exact (wrong location = mismatch), mode and read-back kept;
// on error the file is untouched.
func TestEditAccuracySuite(t *testing.T) {
	t.Parallel()
	cases := loadSuite(t)
	if len(cases) < 330 {
		t.Fatalf("suite has %d cases, want ≥ 330", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			runSuiteCase(t, &c)
		})
	}
}

func runSuiteCase(t *testing.T, c *suiteCase) {
	v := newEnv(t)
	path := filepath.Join(v.root, "f.txt")
	writeInput(t, c, path)
	if c.Xattr {
		setXattr(t, path)
	}
	if c.Mode != "" && runtime.GOOS != "windows" {
		m, err := strconv.ParseUint(c.Mode, 8, 32)
		if err == nil {
			err = os.Chmod(path, os.FileMode(m))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	target := suiteSetup(t, v, c, path)
	before := fileSum(t, path)
	modeBefore := mustStat(t, path).Mode()
	res, err := runSuiteTool(t, v, c, target, path, &before)
	wantCode(t, err, c.WantCode)
	if te, ok := errors.AsType[*ToolError](err); ok && (!strings.Contains(te.Hint, c.WantHint) || !strings.Contains(te.Message, c.WantMsg)) {
		t.Errorf("hint %q / msg %q, want %q / %q", te.Hint, te.Message, c.WantHint, c.WantMsg)
	}
	checkSuiteFile(t, c, path, before, err)
	if err == nil {
		suiteAfter(t, v, c, path, modeBefore, &res)
	}
}

// runSuiteTool takes the tag (as the model would), applies a mutation,
// runs the tool and, for retry cases, retries with the fresh tag.
func runSuiteTool(t *testing.T, v *env, c *suiteCase, target, path string, before *string) (Result, error) {
	ctx := context.Background()
	o := Opts{Approved: c.Approved}
	tag := c.TagOverride
	if (c.Tool == "edit" || c.Tag) && tag == "" {
		r, err := v.e.Read(ctx, ReadArgs{Path: target}, o)
		if err != nil {
			return r, err
		}
		tag = metaStr(&r, "tag")
	}
	if c.Mutate != nil {
		replaceFile(t, path, c.Mutate.bytes(t))
		*before = fileSum(t, path)
	}
	run := func(tag string) (Result, error) {
		if c.Tool == "write" {
			a := WriteArgs{Path: target, Content: c.Content}
			if c.Tag {
				a.Tag = &tag
			}
			return v.e.Write(ctx, a, o)
		}
		return v.e.Edit(ctx, EditArgs{Path: target, Tag: tag, Edits: c.Edits}, o)
	}
	res, err := run(tag)
	if !c.Retry {
		return res, err
	}
	wantCode(t, err, CodeStaleRead)
	m := regexp.MustCompile(`now #([0-9a-f]{8})`).FindStringSubmatch(err.Error())
	if m == nil {
		t.Fatalf("stale_read without fresh tag: %v", err)
	}
	return run(m[1])
}

// checkSuiteFile compares the file with the expectation; a failed call
// must leave it byte-identical.
func checkSuiteFile(t *testing.T, c *suiteCase, path, before string, err error) {
	t.Helper()
	switch {
	case c.Want != nil:
		if g, w := readAll(t, path), c.Want.bytes(t); !bytes.Equal(g, w) {
			t.Fatalf("file =\n%q\nwant\n%q", trunc(g), trunc(w))
		}
	case c.WantRepeat != nil:
		if got, w := fileSum(t, path), c.WantRepeat.sum(t); got != w {
			t.Fatalf("large file sha %s, want %s", got, w)
		}
	case err != nil && fileSum(t, path) != before:
		t.Fatalf("failed edit changed the file")
	}
}

func suiteAfter(t *testing.T, v *env, c *suiteCase, path string, modeBefore os.FileMode, res *Result) {
	t.Helper()
	if runtime.GOOS != "windows" && mustStat(t, path).Mode() != modeBefore {
		t.Errorf("mode %v → %v", modeBefore, mustStat(t, path).Mode())
	}
	wantOwner := runtime.GOOS != "windows" || res.Meta["inplace"] == "hardlink" // CT27-12
	if res.Meta["mode_preserved"] != true || res.Meta["owner_preserved"] != wantOwner {
		t.Errorf("meta %v", res.Meta)
	}
	if c.Xattr && runtime.GOOS == "linux" && res.Meta["xattr_preserved"] != true {
		t.Errorf("xattr not preserved: %v", res.Meta)
	}
	checkSuiteSetup(t, v, c, path, res)
	if tag := metaStr(res, "tag"); c.InputRepeat == nil && tag != sumTag(sha256.Sum256(readAll(t, path))) {
		t.Errorf("new tag %s does not match the written bytes", tag)
	}
}

func checkSuiteSetup(t *testing.T, v *env, c *suiteCase, path string, res *Result) {
	t.Helper()
	switch c.Setup {
	case "symlink-in":
		if fi, err := os.Lstat(filepath.Join(v.root, "link.txt")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlink replaced: %v", err)
		}
	case "hardlink":
		if res.Meta["inplace"] != "hardlink" || !bytes.Equal(readAll(t, path), readAll(t, filepath.Join(v.root, "hard.txt"))) {
			t.Errorf("hard link broken: %v", res.Meta)
		}
	}
}

// suiteSetup creates links and returns the path the tool is called with.
func suiteSetup(t *testing.T, v *env, c *suiteCase, path string) string {
	t.Helper()
	switch c.Setup {
	case "symlink-in":
		if err := os.Symlink("f.txt", filepath.Join(v.root, "link.txt")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		return "link.txt"
	case "symlink-out":
		out := filepath.Join(v.out, "f.txt")
		if err := os.Rename(path, out); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(out, path); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		return "f.txt"
	case "hardlink":
		if err := os.Link(path, filepath.Join(v.root, "hard.txt")); err != nil {
			t.Fatalf("link: %v", err)
		}
	}
	return "f.txt"
}

func writeInput(t *testing.T, c *suiteCase, path string) {
	t.Helper()
	if c.InputRepeat == nil {
		writeFile(t, path, string(c.Input.bytes(t)))
		return
	}
	f, err := os.Create(path) //nolint:gosec // G304: fixture paths of this test
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InputRepeat.writeTo(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// replaceFile swaps in new bytes via rename (new inode, editor-style save).
func replaceFile(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := path + ".swap"
	writeFile(t, tmp, string(data))
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func fileSum(t testing.TB, path string) string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: fixture paths of this test
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err == nil {
		err = f.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func readAll(t testing.TB, path string) []byte {
	t.Helper()
	return readFixture(t, path)
}

func trunc(b []byte) []byte {
	if len(b) > 300 {
		return append(b[:300:300], "…"...)
	}
	return b
}

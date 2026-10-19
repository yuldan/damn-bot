package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

//nolint:gochecknoglobals // test flag
var updateGolden = flag.Bool("update", false, "rewrite model-facing golden files")

// goldenDoc is the committed model-facing shape (E-68).
type goldenDoc struct {
	OK      bool       `json:"ok"`
	Content string     `json:"content,omitempty"`
	Error   *goldenErr `json:"error,omitempty"`
}

type goldenErr struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// goldenEnv builds every eager tool with real implementations.
type goldenEnv struct {
	t     *testing.T
	w     *workspace
	d     *Dispatcher
	asker *askerRec
}

func newGolden(t *testing.T) *goldenEnv {
	t.Helper()
	w := newWorkspace(t)
	w.write("a.txt", "alpha\nbeta\n")
	w.write("edit.txt", "one\ntwo\nthree\n")
	w.write("sub/b.txt", "alpha two\n")
	asker := &askerRec{answer: "ok"}
	appr := &approverRec{answer: ApprovalAllowOnce}
	reg := mustReg(t, append(w.tools, NewTodo(), NewClarify(asker))...)
	d, err := NewDispatcher(&Config{
		Registry: reg, Journal: &journalRec{}, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAsk,
		Approver: appr, WorkspaceTrusted: true, Cwd: w.root,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &goldenEnv{t: t, w: w, d: d, asker: asker}
}

// tag reads rel through the pipeline and returns the snapshot tag.
func (g *goldenEnv) tag(rel string) string {
	g.t.Helper()
	out := g.d.Dispatch(context.Background(), Call{Name: "read_file", Args: json.RawMessage(`{"path":` + quoteJSON(rel) + `}`)})
	if out.Err != nil {
		g.t.Fatalf("tag read: %v", out.Err)
	}
	tag, ok := out.Meta["tag"].(string)
	if !ok || tag == "" {
		g.t.Fatalf("no tag in %v", out.Meta)
	}
	return tag
}

type goldenCase struct {
	name string
	args func(*goldenEnv) string
}

//nolint:gochecknoglobals // table of golden cases
var goldenCases = map[string][]goldenCase{
	"read_file": {
		{"success", func(*goldenEnv) string { return `{"path":"a.txt"}` }},
		{"missing_path", func(*goldenEnv) string { return `{}` }},
	},
	"write_file": {
		{"success", func(*goldenEnv) string { return `{"path":"new.txt","content":"hello\n"}` }},
		{"stale_read", func(*goldenEnv) string { return `{"path":"a.txt","content":"replace\n"}` }},
	},
	"edit_file": {
		{"success", func(g *goldenEnv) string {
			return `{"path":"edit.txt","tag":"` + g.tag("edit.txt") + `","edits":[{"old_string":"two","new_string":"TWO"}]}`
		}},
		{"stale_read", func(*goldenEnv) string {
			return `{"path":"edit.txt","tag":"00000000","edits":[{"old_string":"two","new_string":"TWO"}]}`
		}},
	},
	"glob": {
		{"success", func(*goldenEnv) string { return `{"pattern":"**/*.txt"}` }},
		{"bad_pattern", func(*goldenEnv) string { return `{"pattern":"["}` }},
	},
	"grep": {
		{"success", func(*goldenEnv) string { return `{"pattern":"alpha"}` }},
		{"bad_regex", func(*goldenEnv) string { return `{"pattern":"("}` }},
	},
	"todo": {
		{"success", func(*goldenEnv) string {
			return `{"items":[{"id":"1","text":"inspect","status":"done"},{"id":"2","text":"edit","status":"in_progress"}]}`
		}},
		{"duplicate_id", func(*goldenEnv) string {
			return `{"items":[{"id":"1","text":"a","status":"pending"},{"id":"1","text":"b","status":"done"}]}`
		}},
	},
	"clarify": {
		{"success", func(*goldenEnv) string { return `{"question":"which file?","choices":["a","b"]}` }},
		{"empty_question", func(*goldenEnv) string { return `{"question":""}` }},
	},
}

var durationRE = regexp.MustCompile(`\b\d+ ms\b`)

//nolint:gocognit // table-driven golden runner
func TestGolden(t *testing.T) {
	for _, tool := range mustReg(t, allGoldenTools(t)...).Names() {
		cases := goldenCases[tool]
		if len(cases) == 0 {
			t.Fatalf("%s has no golden cases", tool)
		}
		for _, c := range cases {
			t.Run(tool+"/"+c.name, func(t *testing.T) {
				g := newGolden(t)
				args := c.args(g)
				out := g.d.Dispatch(context.Background(), Call{Name: tool, Args: json.RawMessage(args)})
				got := renderGolden(out, g)
				path := filepath.Join("testdata", "golden", tool, c.name+".json")
				if *updateGolden {
					if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, got, 0o600); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path) //nolint:gosec // testdata path
				if err != nil {
					t.Fatalf("read golden: %v (run go test -update)", err)
				}
				if !bytes.Equal(want, got) {
					t.Fatalf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
				}
			})
		}
	}
}

// TestGoldenCoverage enforces E-68: every registered eager tool has a
// success golden and at least one error golden.
//
//nolint:gocognit // coverage scan with per-tool assertions
func TestGoldenCoverage(t *testing.T) {
	names := mustReg(t, allGoldenTools(t)...).Names()
	for _, name := range names {
		dir := filepath.Join("testdata", "golden", name)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s: %v (run go test -update)", name, err)
		}
		var ok, bad int
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // testdata path
			if err != nil {
				t.Fatal(err)
			}
			var doc goldenDoc
			if err := json.Unmarshal(b, &doc); err != nil {
				t.Fatalf("%s/%s: %v", name, e.Name(), err)
			}
			if doc.OK {
				ok++
			} else {
				bad++
				if doc.Error == nil || !CodeSet()[doc.Error.Code] {
					t.Fatalf("%s/%s: error code outside the closed set", name, e.Name())
				}
			}
		}
		if ok == 0 || bad == 0 {
			t.Fatalf("%s: %d success, %d error goldens; want ≥1 each", name, ok, bad)
		}
	}
}

func allGoldenTools(t *testing.T) []Tool {
	t.Helper()
	w := newWorkspace(t)
	return append(w.tools, NewTodo(), NewClarify(&askerRec{answer: "ok"}))
}

func renderGolden(out Outcome, g *goldenEnv) []byte {
	doc := goldenDoc{OK: out.Err == nil}
	if out.Err == nil {
		content := strings.ReplaceAll(out.Content, "\r\n", "\n")
		content = strings.ReplaceAll(content, g.w.root, "<root>")
		content = strings.ReplaceAll(content, g.w.out, "<outside>")
		content = durationRE.ReplaceAllString(content, "<ms> ms")
		doc.Content = content
	} else {
		doc.Error = &goldenErr{Code: out.Err.Code, Message: out.Err.Message, Hint: out.Err.Hint}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		g.t.Fatal(err)
	}
	return buf.Bytes()
}

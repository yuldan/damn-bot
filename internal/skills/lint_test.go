package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// corpusSkill parses testdata/lint/<kind>/<name>/SKILL.md and opens its
// directory through os.Root, as the composition root does (ADR-133).
func corpusSkill(t *testing.T, kind, name string, src Source) (Skill, fs.FS) {
	t.Helper()
	dir := filepath.Join("testdata", "lint", kind, name)
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md")) //nolint:gosec // testdata path
	if err != nil {
		t.Fatal(err)
	}
	sk, err := Parse(data, filepath.Join(dir, "SKILL.md"), src)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	return sk, root.FS()
}

func corpusKnown(name string) bool { return name == "plain-review" }

func categories(list []Finding) []string {
	out := make([]string, 0, len(list))
	for _, f := range list {
		out = append(out, f.Category+":"+f.Severity)
	}
	return out
}

// TestLintCorpus: every good skill lints clean for every source (its only
// findings are the expected advisory ones) and every bad skill is either
// invalid_skill or carries the expected finding (SEC-26, SEC-15).
func TestLintCorpus(t *testing.T) {
	t.Parallel()
	good := map[string][]string{
		"plain-review":   {},
		"with-script":    {},
		"stripe-sandbox": {"payment:medium"},
		"solana-devnet":  {"payment:medium"},
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "lint", "good"))
	if err != nil || len(entries) != len(good) {
		t.Fatalf("good corpus = %d entries (%v), table = %d", len(entries), err, len(good))
	}
	for name, want := range good {
		for _, src := range []Source{SourceBuiltin, SourceUser, SourceHub} {
			sk, files := corpusSkill(t, "good", name, src)
			got, lerr := Lint(&sk, LintEnv{Files: files, Known: corpusKnown})
			if lerr != nil || !slices.Equal(categories(got), want) {
				t.Errorf("good %s (%s): findings %v err %v, want %v", name, src, categories(got), lerr, want)
			}
		}
	}

	bad := []struct {
		name    string
		src     Source
		wantErr string   // invalid_skill detail, or "" for findings
		want    []string // findings when valid
	}{
		{"missing-script", SourceBuiltin, "scripts/absent.sh does not exist", nil},
		{"parent-path", SourceHub, "outside the skill directory", nil},
		{"absolute-path", SourceHub, "outside the skill directory", nil},
		{"home-path", SourceUser, "outside the skill directory", nil},
		{"placeholder-escape", SourceImport, "outside the skill directory", nil},
		{"unknown-require", SourceBuiltin, `requires unknown skill "ghost-skill"`, nil},
		{"dangling-link", SourceHub, "skill://ghost-skill does not resolve", nil},
		{"mainnet", SourceBuiltin, "builtin skill defaults to mainnet", nil},
		{"mainnet", SourceHub, "", []string{"payment:medium", "mainnet_default:medium"}},
		{"reads-env", SourceHub, "", []string{"credential_access:high"}},
		{"script-reads-env", SourceHub, "", []string{"credential_access:high"}},
		{"git-credentials", SourceAgent, "", []string{"credential_access:high"}},
		{"keychain", SourceHub, "", []string{"credential_access:high"}},
		{"gh-token", SourceImport, "", []string{"credential_access:high"}},
		{"prints-secret", SourceHub, "", []string{"credential_access:high"}},
		{"env-dump", SourceHub, "", []string{"credential_access:high"}},
		{"tell-secret", SourceHub, "", []string{"credential_access:high"}},
		{"payment-undeclared", SourceHub, "", []string{"payment_undeclared:medium"}},
		{"unreferenced-helper", SourceHub, "", []string{"credential_access:high"}},
		{"homoglyph", SourceImport, "", []string{"credential_access:high"}},
		{"base64-exec", SourceHub, "", []string{"obfuscated_exec:medium"}},
		{"tools-helper", SourceHub, "", []string{"payment_undeclared:medium"}},
		{"hidden-cred", SourceHub, "", []string{"credential_access:high"}},
		{"hidden-pay", SourceHub, "", []string{"payment_undeclared:medium"}},
		{"obf-concat", SourceHub, "", []string{"credential_access:high"}},
		{"obf-b64decode", SourceHub, "", []string{"obfuscated_exec:medium"}},
		{"obf-zcat", SourceHub, "", []string{"obfuscated_exec:medium"}},
		{"homoglyph-g", SourceHub, "", []string{"payment_undeclared:medium"}},
	}
	entries, err = os.ReadDir(filepath.Join("testdata", "lint", "bad"))
	if err != nil || len(entries) != len(bad)-1 { // mainnet is listed twice
		t.Fatalf("bad corpus = %d entries (%v), table = %d", len(entries), err, len(bad)-1)
	}
	for _, c := range bad {
		sk, files := corpusSkill(t, "bad", c.name, c.src)
		got, err := Lint(&sk, LintEnv{Files: files, Known: corpusKnown})
		switch {
		case c.wantErr != "":
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("bad %s: err %v, want invalid_skill %q", c.name, err, c.wantErr)
			}
		case err != nil || !slices.Equal(categories(got), c.want):
			t.Errorf("bad %s (%s): findings %v err %v, want %v", c.name, c.src, categories(got), err, c.want)
		}
	}
}

// TestLintFailsClosed: without resolvers every reference is invalid; a
// symlinked script that leaves the skill is never read; scripts are capped.
func TestLintFailsClosed(t *testing.T) {
	t.Parallel()
	sk, _ := corpusSkill(t, "good", "with-script", SourceHub)
	if _, err := Lint(&sk, LintEnv{Known: corpusKnown}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cannot be verified") {
		t.Errorf("nil Files: %v", err)
	}
	if _, err := Lint(&sk, LintEnv{Files: fstest.MapFS{}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "requires unknown") {
		t.Errorf("nil Known: %v", err)
	}

	big := fstest.MapFS{"scripts/x.sh": {Data: []byte(strings.Repeat("#", MaxScriptBytes+1))}}
	sk.Body, sk.Requires = "Run scripts/x.sh.", nil
	if _, err := Lint(&sk, LintEnv{Files: big}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("oversized script: %v", err)
	}
	dirFS := fstest.MapFS{"scripts/x.sh": {Mode: fs.ModeDir}}
	if _, err := Lint(&sk, LintEnv{Files: dirFS}); !errors.Is(err, ErrInvalid) {
		t.Errorf("directory as script: %v", err)
	}

	base := t.TempDir()
	outside := filepath.Join(base, "outside.sh")
	if err := os.WriteFile(outside, []byte("cat .env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "skill")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "scripts", "x.sh")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	got, err := Lint(&sk, LintEnv{Files: root.FS()})
	if !errors.Is(err, ErrInvalid) || len(got) != 0 {
		t.Errorf("symlink escape: findings %v err %v, want invalid_skill without reading the target", got, err)
	}
}

// TestLintBounds: the directory walk is bounded and fails closed beyond the
// cap (SEC10-2), and an unverifiable directory is invalid even without
// references.
func TestLintBounds(t *testing.T) {
	t.Parallel()
	clean, _ := corpusSkill(t, "good", "plain-review", SourceUser)
	if _, err := Lint(&clean, LintEnv{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cannot be verified") {
		t.Errorf("nil Files without references: %v", err)
	}
	many := fstest.MapFS{}
	for i := range MaxScanFiles + 1 {
		many[fmt.Sprintf("f%02d", i)] = &fstest.MapFile{Data: []byte("x")}
	}
	if _, err := Lint(&clean, LintEnv{Files: many}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("file cap: %v", err)
	}
	huge := fstest.MapFS{"big": {Data: make([]byte, MaxScanBytes+1)}}
	if _, err := Lint(&clean, LintEnv{Files: huge}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("byte cap: %v", err)
	}
}

// TestLintNormalization: obfuscation folding applies to constructed skills
// too (zero-width characters are normally rejected by Parse, SEC10-4).
func TestLintNormalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body string
		want       []string
	}{
		{"zero-width", "Run `gh au\u200bth token`.", []string{"credential_access:high"}},
		{"concat", "Run `a=gh; $a auth token`.", []string{"credential_access:high"}},
		{"b64decode", "os.system(base64.b64decode(x))", []string{"obfuscated_exec:medium"}},
		{"zcat", "zcat tool.gz | sh", []string{"obfuscated_exec:medium"}},
		{"cyrillic-g", "stгipe payment_intents create", []string{"payment_undeclared:medium"}},
		{"fullwidth", "ｇｈ ａｕｔｈ ｔｏｋｅｎ", []string{"credential_access:high"}},
	}
	for _, c := range cases {
		sk := Skill{Name: "x", Description: "d", Body: c.body, Path: "/s/x/SKILL.md", SHA256: strings.Repeat("a", 64), Source: SourceHub}
		got, err := Lint(&sk, LintEnv{Files: fstest.MapFS{}})
		if err != nil || !slices.Equal(categories(got), c.want) {
			t.Errorf("%s: findings %v err %v, want %v", c.name, categories(got), err, c.want)
		}
	}
}

// TestParseSideEffects: inline lists only, closed set, valid requires.
func TestParseSideEffects(t *testing.T) {
	t.Parallel()
	head := "---\nname: s\ndescription: d\n"
	ok := []struct {
		fm      string
		effects []SideEffect
		payment bool
	}{
		{"side_effects: [payment, network]\n", []SideEffect{EffectPayment, EffectNetwork}, true},
		{"side_effects: payment\n", []SideEffect{EffectPayment}, true},
		{"side_effects: ['write']\n", []SideEffect{EffectWrite}, false},
		{"side_effects: []\n", []SideEffect{}, false},
		{"", nil, false},
	}
	for _, c := range ok {
		sk, err := Parse([]byte(head+c.fm+"---\nbody\n"), "/s/SKILL.md", SourceHub)
		if err != nil || !slices.Equal(sk.SideEffects, c.effects) || (sk.SideEffects == nil) != (c.effects == nil) || sk.Payment() != c.payment {
			t.Errorf("%q: %+v %v", c.fm, sk.SideEffects, err)
		}
	}
	bad := []struct {
		fm   string
		want error
	}{
		{"side_effects: [money]\n", ErrInvalid},
		{"side_effects:\n  - payment\n", ErrFrontMatter}, // block list would drop the declaration
		{"side_effects: [payment\n", ErrFrontMatter},
		{"requires: [Bad Name]\n", ErrInvalid},
		{"requires:\n", ErrFrontMatter},
	}
	for _, c := range bad {
		if _, err := Parse([]byte(head+c.fm+"---\nbody\n"), "/s/SKILL.md", SourceHub); !errors.Is(err, c.want) {
			t.Errorf("%q: err %v, want %v", c.fm, err, c.want)
		}
	}
	sk, err := Parse([]byte(head+"requires: [a, b-c]\n---\nbody\n"), "/s/x/SKILL.md", SourceUser)
	if err != nil || !slices.Equal(sk.Requires, []string{"a", "b-c"}) {
		t.Fatalf("requires: %v %v", sk.Requires, err)
	}
	plain := sk
	plain.Path = "/s/y/SKILL.md"
	sk.SideEffects = []SideEffect{EffectPayment}
	pending := sk
	pending.MemoryID = 99 // quarantined, not approved: still gated
	undeclared := plain
	undeclared.Path = "/s/z/SKILL.md"
	undeclared.Body = "run stripe payment_intents create"
	want := []string{filepath.Dir("/s/x/SKILL.md"), filepath.Dir("/s/z/SKILL.md")}
	if got := PaymentDirs([]Skill{sk, plain, pending, undeclared}); !slices.Equal(got, want) {
		t.Errorf("PaymentDirs = %v, want %v", got, want)
	}
}

// installCorpus installs one corpus skill through a Service over fakeStore
// with a scanner that always reports one medium url finding.
func installCorpus(t *testing.T, kind, name string, src Source) (*fakeStore, Install, error) {
	t.Helper()
	scanner := func(*Skill) (string, string, error) {
		return "medium", `{"severity":"medium","findings":[{"category":"url","severity":"medium"}]}`, nil
	}
	st := &fakeStore{}
	sk, files := corpusSkill(t, kind, name, src)
	svc, err := New(st, Config{
		Scan: scanner, Redact: func(s string) string { return s }, Known: corpusKnown,
		Files: func(*Skill) (fs.FS, error) { return files, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Install(context.Background(), &sk)
	return st, res, err
}

// TestInstallLintInvalid: broken references, builtin mainnet defaults and
// trusted credential access are invalid_skill and store nothing.
func TestInstallLintInvalid(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		src  Source
	}{{"missing-script", SourceUser}, {"dangling-link", SourceHub}, {"reads-env", SourceBuiltin}, {"keychain", SourceUser}, {"mainnet", SourceBuiltin}} {
		st, _, err := installCorpus(t, "bad", c.name, c.src)
		if !errors.Is(err, ErrInvalid) || len(st.indexed) != 0 || st.quarantined != nil {
			t.Errorf("%s (%s): err %v, stored %d", c.name, c.src, err, len(st.indexed))
		}
	}
	st, res, err := installCorpus(t, "good", "with-script", SourceBuiltin)
	if err != nil || res.Status != StatusApproved || len(st.indexed) != 1 {
		t.Fatalf("good builtin: %+v %v", res, err)
	}
}

// TestInstallLintFindings: untrusted credential access is auto-rejected with
// the finding merged into the scanner's scan_result; an undeclared payment
// is stored for review with a medium finding.
func TestInstallLintFindings(t *testing.T) {
	t.Parallel()
	st, res, err := installCorpus(t, "bad", "script-reads-env", SourceHub)
	if err != nil || res.Status != StatusRejected || !st.rejected {
		t.Fatalf("untrusted credential access: %+v %v", res, err)
	}
	var scan struct {
		Severity string    `json:"severity"`
		Findings []Finding `json:"findings"`
	}
	if jerr := json.Unmarshal([]byte(st.scanResult), &scan); jerr != nil || scan.Severity != "high" ||
		!slices.Equal(categories(scan.Findings), []string{"url:medium", "credential_access:high"}) {
		t.Fatalf("scan_result = %s (%v)", st.scanResult, jerr)
	}
	st, res, err = installCorpus(t, "bad", "payment-undeclared", SourceHub)
	if err != nil || res.Status != StatusScanned || st.rejected || !strings.Contains(st.scanResult, `"payment_undeclared"`) {
		t.Fatalf("undeclared payment: %+v %v %s", res, err, st.scanResult)
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeVerifyFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDetectVerifyRecipe pins the detection order and the argv each manifest
// produces; the argv is literal, so no manifest can inject a shell.
func TestDetectVerifyRecipe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files map[string]string
		kind  string
		argv  string
	}{
		{name: "go", files: map[string]string{"go.mod": "module x\n"}, kind: "go", argv: "go\x00test\x00./..."},
		{name: "cargo", files: map[string]string{"Cargo.toml": "[package]\n"}, kind: "cargo", argv: "cargo\x00test"},
		{name: "npm", files: map[string]string{"package.json": `{"scripts":{"test":"jest"}}`}, kind: "node", argv: "npm\x00test"},
		{name: "pnpm", files: map[string]string{"package.json": `{"scripts":{"test":"vitest"}}`, "pnpm-lock.yaml": ""}, kind: "node", argv: "pnpm\x00test"},
		{name: "python", files: map[string]string{"pyproject.toml": "[tool.pytest.ini_options]\n"}, kind: "pytest", argv: "python\x00-m\x00pytest\x00-q"},
		{name: "python-tests", files: map[string]string{"tests/test_api.py": ""}, kind: "pytest", argv: "python\x00-m\x00pytest\x00-q"},
		{name: "make", files: map[string]string{"Makefile": "all:\n\ttrue\ntest:\n\ttrue\n"}, kind: "make", argv: "make\x00test"},
		{name: "just", files: map[string]string{"justfile": "test:\n  true\n"}, kind: "just", argv: "just\x00test"},
		{name: "go-before-make", files: map[string]string{"go.mod": "module x\n", "Makefile": "test:\n\ttrue\n"}, kind: "go", argv: "go\x00test\x00./..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, body := range tc.files {
				writeVerifyFile(t, dir, name, body)
			}
			recipe, ok := detectVerifyRecipe(dir)
			if !ok {
				t.Fatal("no recipe detected")
			}
			if recipe.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", recipe.Kind, tc.kind)
			}
			if got := strings.Join(recipe.argv, "\x00"); got != tc.argv {
				t.Errorf("argv = %q, want %q", got, tc.argv)
			}
		})
	}
}

// TestDetectVerifyRecipeFallsThrough: a manifest without a runnable test is
// not a recipe; detection continues to the next candidate.
func TestDetectVerifyRecipeFallsThrough(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeVerifyFile(t, dir, "package.json", `{"scripts":{"build":"tsc"}}`)
	writeVerifyFile(t, dir, "Makefile", "build:\n\ttrue\n")
	if recipe, ok := detectVerifyRecipe(dir); ok {
		t.Fatalf("recipe = %+v, want none", recipe)
	}
}

func TestVerifyCLIRejectsBadInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if code, _, errOut := runArgs(t, "verify", "--path", filepath.Join(dir, "missing")); code != 1 || !strings.Contains(errOut, "path") {
		t.Fatalf("missing path = %d %q", code, errOut)
	}
	file := filepath.Join(dir, "file")
	writeVerifyFile(t, dir, "file", "")
	if code, _, _ := runArgs(t, "verify", "--path", file); code != 1 {
		t.Fatalf("file path = %d", code)
	}
	if code, _, _ := runArgs(t, "verify", "--timeout", "0"); code != 2 {
		t.Fatalf("zero timeout = %d", code)
	}
	if code, _, _ := runArgs(t, "verify", "extra"); code != 2 {
		t.Fatalf("extra arg = %d", code)
	}
}

func TestVerifyCLINoRecipe(t *testing.T) {
	t.Parallel()
	code, out, errOut := runArgs(t, "verify", "--path", t.TempDir())
	if code != 1 || !strings.Contains(errOut, "no run recipe") {
		t.Fatalf("verify empty = %d out=%q err=%q", code, out, errOut)
	}
}

func TestVerifyCLIJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeVerifyFile(t, dir, "go.mod", "module example.com/verify\n")
	code, out, errOut := runArgs(t, "verify", "--path", dir, "--json")
	if code != 0 {
		t.Fatalf("verify --json = %d %q", code, errOut)
	}
	var rep verifyReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("json: %v (%q)", err, out)
	}
	if !rep.Found || rep.Recipe == nil || rep.Recipe.Kind != "go" || rep.Recipe.Command != "go test ./..." {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Run != nil {
		t.Fatalf("run = %+v, want nil without --run", rep.Run)
	}
}

// TestVerifyCLIRunSmoke executes the real detected recipe in a throwaway Go
// module: the pass path is the parity acceptance for CLI-033/OPS-025.
func TestVerifyCLIRunSmoke(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeVerifyFile(t, dir, "go.mod", "module example.com/verifypass\n\ngo 1.26.11\n")
	writeVerifyFile(t, dir, "pass_test.go", "package verifypass\n\nimport \"testing\"\n\nfunc TestPass(t *testing.T) {}\n")
	code, out, errOut := runArgs(t, "verify", "--path", dir, "--run", "--timeout", "2m")
	if code != 0 {
		t.Fatalf("verify --run = %d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "smoke: pass") {
		t.Fatalf("output = %q", out)
	}
}

func TestVerifyCLIRunFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeVerifyFile(t, dir, "go.mod", "module example.com/verifyfail\n\ngo 1.26.11\n")
	writeVerifyFile(t, dir, "fail_test.go", "package verifyfail\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"boom\") }\n")
	code, out, _ := runArgs(t, "verify", "--path", dir, "--run", "--timeout", "2m")
	if code != 1 || !strings.Contains(out, "smoke: fail") {
		t.Fatalf("verify failing recipe = %d out=%q", code, out)
	}
}

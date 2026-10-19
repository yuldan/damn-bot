package classify

import (
	"slices"
	"testing"
)

// allowNegatives are "go test ./..." variants with a metacharacter,
// expansion, redirect, assignment, pipeline, control character or other
// shape that must never match the entry ["go","test","./..."].
func allowNegatives() []string {
	return []string{
		"go test ./... | tee out", "go test ./... |& cat", "go test ./...; ls", "go test ./...;",
		"go test ./... &", "go test ./... && ls", "go test ./... || ls", "(go test ./...)",
		"{ go test ./...; }", "go test $PWD/...", "go test ${X}", "go test `pwd`/...",
		"go test $(pwd)/...", "go test \"$X\"", "go test $'./...'", "go test ~/x", "go test ./*",
		"go test ./?", "go test ./[a]", "go test ./{a,b}", "go test ./... > out", "go test ./... >> out",
		"go test ./... < in", "go test ./... 2>&1", "go test ./... &> out", "GOFLAGS=-x go test ./...",
		"X= go test ./...", "go test ./... <<EOF\nx\nEOF", "go test ./... <<< x", "! go test ./...",
		"go test ./... #c", "go test \\./...", "go test ./...\r", "go test ./...\x00", "go test ./...\n",
		"go test ./...\nls", "go test ./...\x1b[0m", "go test ./...\x7f", "go test ./...\u00a0",
		"go test ./...\u200b", "go test ./... \u202e", "go test ./... -v", "go test", "go test ./... \\\n",
		"time go test ./...", "coproc go test ./...", "if go test ./...; then :; fi", "go test !x",
		"./go test ./...", "/usr/bin/go test ./...", "go test ./...)", "go 'test ./...'", "",
		"go test ./... $", "exec go test ./...", "command go test ./...",
	}
}

// TestShellAllowExactMatch (ADR-079 butir 3, ADR-088, NEW3-04).
func TestShellAllowExactMatch(t *testing.T) {
	t.Parallel()
	entry := AllowEntry{"go", "test", "./..."}
	n := 0
	for _, cmd := range allowNegatives() {
		n++
		if MatchAllow(allowRequest(cmd, entry)) {
			t.Errorf("%q matched", cmd)
		}
	}
	for _, cmd := range []string{"go test ./...", "go  test\t./...", "'go' \"test\" './...'", "go test ./... "} {
		n++
		if !MatchAllow(allowRequest(cmd, entry)) {
			t.Errorf("%q did not match", cmd)
		}
	}
	n += binaryCases(t)
	n += windowsCases(t)
	if n < 60 {
		t.Fatalf("%d cases < 60", n)
	}
}

// binaryCases: argv0 must resolve to the same absolute binary as at
// session start, outside the root and cwd (NEW3-04).
func binaryCases(t *testing.T) int {
	t.Helper()
	start, _ := goBinary("go")
	cases := map[string]struct {
		resolved BinaryID
		ok       bool
		want     bool
	}{
		"same binary":          {start, true, true},
		"changed inode":        {BinaryID{Path: start.Path, Dev: 1, Ino: 43, Size: start.Size, MtimeNs: 7}, true, false},
		"changed mtime":        {BinaryID{Path: start.Path, Dev: 1, Ino: 42, Size: start.Size, MtimeNs: 8}, true, false},
		"other path":           {BinaryID{Path: "/opt/go/bin/go", Dev: 1, Ino: 42, Size: start.Size, MtimeNs: 7}, true, false},
		"unresolved":           {BinaryID{}, false, false},
		"inside root":          {BinaryID{Path: testRoot + "/bin/go"}, true, false},
		"relative PATH entry":  {BinaryID{Path: "bin/go"}, true, false},
		"script launcher .bat": {BinaryID{Path: "/usr/bin/go.bat"}, true, false},
	}
	for name, c := range cases {
		r := allowRequest("go test ./...", AllowEntry{"go", "test", "./..."})
		r.Resolve = func(string) (BinaryID, bool) { return c.resolved, c.ok }
		if name != "same binary" && name != "changed inode" && name != "changed mtime" && name != "other path" {
			r.SessionStart = map[string]BinaryID{"go": c.resolved}
		}
		if got := MatchAllow(r); got != c.want {
			t.Errorf("%s: match = %v, want %v", name, got, c.want)
		}
	}
	r := allowRequest("go test ./...", AllowEntry{"go", "test", "./..."})
	r.Resolve = nil
	r2 := allowRequest("go test ./...", AllowEntry{"go", "test", "./..."})
	r2.SessionStart = nil
	if MatchAllow(r) || MatchAllow(r2) {
		t.Error("match without resolver or session start record")
	}
	return len(cases) + 2
}

// windowsCases: CreateProcess of a .exe only; .bat/.cmd and the cwd never
// match.
func windowsCases(t *testing.T) int {
	t.Helper()
	cases := []struct {
		cmd, bin string
		d        Dialect
		want     bool
	}{
		{"go test ./...", `C:\Go\bin\go.exe`, Pwsh, true},
		{"go test ./...", `C:\Go\bin\go.exe`, Cmd, true},
		{"go test ./...", `C:\tools\go.bat`, Cmd, false},
		{"go test ./...", `C:\tools\go.cmd`, Pwsh, false},
		{"go test ./...", `C:\PROJ\go.exe`, Cmd, false},
		{"go test ./...", `C:\proj\sub\go.exe`, Pwsh, false},
		{"go test ./... | x", `C:\Go\bin\go.exe`, Pwsh, false},
		{"go test %X%", `C:\Go\bin\go.exe`, Cmd, false},
		{"go test $env:X", `C:\Go\bin\go.exe`, Pwsh, false},
		{"go test ^& x", `C:\Go\bin\go.exe`, Cmd, false},
		{"go test \x60./...", `C:\Go\bin\go.exe`, Pwsh, false},
		{"go test './...'", `C:\Go\bin\go.exe`, Pwsh, false},
		{"go test @x", `C:\Go\bin\go.exe`, Pwsh, false},
		{"go test ./...,x", `C:\Go\bin\go.exe`, Pwsh, false},
		{`.\go test ./...`, `C:\Go\bin\go.exe`, Pwsh, false},
	}
	for _, c := range cases {
		id := BinaryID{Path: c.bin, Ino: 9}
		r := &AllowRequest{
			Command: c.cmd, Dialect: c.d, Cwd: `C:\proj`, Roots: []string{`C:\proj`},
			Entries: []AllowEntry{{"go", "test", "./..."}}, SessionStart: map[string]BinaryID{"go": id},
			Resolve: func(string) (BinaryID, bool) { return id, true },
		}
		if got := MatchAllow(r); got != c.want {
			t.Errorf("%q -> %s (%d): match = %v, want %v", c.cmd, c.bin, c.d, got, c.want)
		}
	}
	return len(cases)
}

// TestShellAllowCwdTracked: a match requires the call cwd inside a root.
func TestShellAllowCwdTracked(t *testing.T) {
	t.Parallel()
	for cwd, want := range map[string]bool{
		testRoot:                 true,
		testRoot + "/sub":        true,
		testRoot + "/sub/../src": true,
		"/tmp":                   false,
		testRoot + "/../x":       false,
		testRoot + "2":           false,
		"proj":                   false,
		"":                       false,
		"/":                      false,
	} {
		r := allowRequest("go test ./...", AllowEntry{"go", "test", "./..."})
		r.Cwd = cwd
		if got := MatchAllow(r); got != want {
			t.Errorf("cwd %q: match = %v, want %v", cwd, got, want)
		}
	}
	r := &AllowRequest{
		Command: "go test ./...", Dialect: Cmd, Cwd: `c:\PROJ\Sub`, Roots: []string{`C:\proj`},
		Entries: []AllowEntry{{"go", "test", "./..."}}, SessionStart: map[string]BinaryID{"go": {Path: `C:\Go\bin\go.exe`}},
		Resolve: func(string) (BinaryID, bool) { return BinaryID{Path: `C:\Go\bin\go.exe`}, true },
	}
	if !MatchAllow(r) {
		t.Error("Windows cwd with other case/separators not inside root (EC-WIN-013)")
	}
}

func TestDirectArgv(t *testing.T) {
	t.Parallel()
	for cmd, want := range map[string][]string{
		"go test ./...":          {"go", "test", "./..."},
		`git commit -m "fix: x"`: {"git", "commit", "-m", "fix: x"},
		"echo 'a\"b'":            {"echo", `a"b`},
		"go test ./... | x":      nil,
		"   ":                    nil,
	} {
		got, ok := DirectArgv(cmd, Bash)
		if ok != (want != nil) || !slices.Equal(got, want) {
			t.Errorf("DirectArgv(%q) = %q %v, want %q", cmd, got, ok, want)
		}
	}
	if got, ok := DirectArgv("ls -la", POSIX); !ok || len(got) != 2 {
		t.Errorf("posix = %q %v", got, ok)
	}
	if _, ok := DirectArgv("[[ -f x ]]", POSIX); ok {
		t.Error("bash-only syntax accepted as POSIX")
	}
}

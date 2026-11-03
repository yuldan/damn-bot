//go:build !minimal

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules"
)

// isF6Group is the closed set of F6 deferred-catalog groups. A descriptor of
// another group (MCP, memory, …) is outside this lock.
func isF6Group(group string) bool {
	switch group {
	case "web", "browser", "media", "voice", "computer_use", "computeruse":
		return true
	}
	return false
}

// toolsLockEntry is one declarative inventory row.
type toolsLockEntry struct {
	Module, Tool string
	Version      int
	SHA256       string
}

// TestToolsLockComplete (GAP-TOOL-06) pins the module tool inventory: every
// tool an F6 descriptor contributes must be listed in tools.lock with the
// same module and schema hash, and the lock must not name a tool that no
// descriptor contributes. Registration is data-only, so this test never
// starts a module.
func TestToolsLockComplete(t *testing.T) {
	t.Parallel()
	lock := readToolsLock(t, filepath.Join("..", "..", "tools.lock"))
	for _, problem := range toolsLockProblems(lock, modules.Default()) {
		t.Error(problem)
	}
}

// TestToolsLockRejectsMissingModuleTool is the negative control of
// TestToolsLockComplete: dropping one registered line (browser W2, media W3,
// voice W4, computer-use W5) must fail, so a green lock test proves every
// entry is actually compared.
func TestToolsLockRejectsMissingModuleTool(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"browser_click", "browser_console", "browser_dialog", "browser_cdp", "browser_vault", "image_generate", "speak", "computer_use"} {
		lock := readToolsLock(t, filepath.Join("..", "..", "tools.lock"))
		delete(lock, tool)
		problems := toolsLockProblems(lock, modules.Default())
		if len(problems) != 1 || !strings.Contains(problems[0], `"`+tool+`"`) {
			t.Fatalf("%s: problems = %v, want the missing entry", tool, problems)
		}
	}
}

// toolsLockProblems compares the declarative inventory with the frozen F6
// descriptors and returns one message per mismatch.
func toolsLockProblems(lock map[string]toolsLockEntry, reg *modules.Registry) []string {
	var out []string
	registered := make(map[string]toolsLockEntry)
	for _, desc := range reg.Descriptors() {
		if !isF6Group(desc.Group) {
			continue
		}
		for _, tool := range desc.Tools {
			sum := sha256.Sum256(tool.Schema)
			registered[tool.Name] = toolsLockEntry{Module: desc.ID, Tool: tool.Name, SHA256: hex.EncodeToString(sum[:])}
		}
	}
	for name, want := range registered {
		got, ok := lock[name]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("tool %q (%s) is registered but missing from tools.lock", name, want.Module))
		case got.Module != want.Module:
			out = append(out, fmt.Sprintf("tool %q: lock module %q, registered module %q", name, got.Module, want.Module))
		case got.SHA256 != want.SHA256:
			out = append(out, fmt.Sprintf("tool %q: schema hash %s in tools.lock, registered %s", name, got.SHA256, want.SHA256))
		}
	}
	for name, entry := range lock {
		if _, ok := registered[name]; !ok {
			out = append(out, fmt.Sprintf("tools.lock names %q (%s) but no F6 descriptor contributes it", name, entry.Module))
		}
	}
	return out
}

// readToolsLock parses the declarative inventory. Blank lines and `#`
// comments are ignored; every other line has exactly four fields.
func readToolsLock(t *testing.T, path string) map[string]toolsLockEntry {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS(filepath.Join("..", "..")), "tools.lock")
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := make(map[string]toolsLockEntry)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 4 {
			t.Errorf("%s:%d: want <module> <tool> <version> <sha256>, got %q", path, line, text)
			continue
		}
		version, verr := strconv.Atoi(fields[2])
		if verr != nil || version < 1 {
			t.Errorf("%s:%d: version %q is not a positive integer", path, line, fields[2])
			continue
		}
		if len(fields[3]) != 64 {
			t.Errorf("%s:%d: schema hash %q is not sha256 hex", path, line, fields[3])
			continue
		}
		if _, dup := out[fields[1]]; dup {
			t.Errorf("%s:%d: duplicate tool %q", path, line, fields[1])
			continue
		}
		out[fields[1]] = toolsLockEntry{Module: fields[0], Tool: fields[1], Version: version, SHA256: fields[3]}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s lists no tool", path)
	}
	return out
}

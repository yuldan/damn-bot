package main

import (
	"bytes"
	"strings"
	"testing"
)

func mcpTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("APPDATA", home)
	t.Setenv("LOCALAPPDATA", home)
	t.Setenv("HOME", home)
}

func runMCPArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := runMCP(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestMCPAuthUsageAndErrors(t *testing.T) {
	mcpTestHome(t)
	cases := [][]string{
		{"auth"},
		{"auth", "nope"},
		{"auth", "login"},
		{"auth", "login", "missing"},
		{"auth", "status", "missing"},
	}
	for _, args := range cases {
		if code, _, _ := runMCPArgs(t, args...); code != 1 && code != 2 {
			t.Fatalf("%q = %d, want 1/2", args, code)
		}
	}
	code, out, errOut := runMCPArgs(t, "auth", "status", "missing")
	if code == 0 || !strings.Contains(out+errOut, "no server selected") {
		t.Fatalf("status missing = %d %q %q", code, out, errOut)
	}
}

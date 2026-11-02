//go:build !minimal

package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGAPF5_COST10StarlarkChild drives the hidden __starlark interpreter over
// a real Unix socket: the tool binding works for an allowed call and surfaces
// network_deny as an error.
func TestGAPF5_COST10StarlarkChild(t *testing.T) {
	dir := t.TempDir()
	allowed := startReplyServer(t, dir, "tool.sock", `{"ok":true,"result":{"path":"x"}}`)
	t.Setenv("DAMNBOT_TOOL_SOCKET", allowed)
	script := writeScript(t, dir, `print(tool("read_file", path="x"))`)
	var out, errOut strings.Builder
	if code := runStarlark([]string{script}, &out, &errOut); code != 0 {
		t.Fatalf("child = %d %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"path":"x"`) {
		t.Fatalf("stdout = %q", out.String())
	}

	denied := startReplyServer(t, dir, "deny.sock", `{"ok":false,"error":"denied","reason":"network_deny"}`)
	t.Setenv("DAMNBOT_TOOL_SOCKET", denied)
	script = writeScript(t, dir, `tool("web_fetch", url="https://example.test")`)
	out.Reset()
	errOut.Reset()
	if code := runStarlark([]string{script}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "network_deny") {
		t.Fatalf("denied child = %d %q", code, errOut.String())
	}
}

func startReplyServer(t *testing.T, dir, name, reply string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	lc := net.ListenConfig{}
	ln, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close listener: %v", cerr)
		}
	})
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer func() {
			if cerr := conn.Close(); cerr != nil {
				return
			}
		}()
		if _, rerr := bufio.NewReader(conn).ReadBytes('\n'); rerr != nil {
			return
		}
		if _, werr := conn.Write([]byte(reply + "\n")); werr != nil {
			return
		}
	}()
	return path
}

func writeScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "main.star")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

//go:build linux

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// TestServeOnlyLoopbackListeners proves on Linux that the serve process owns
// exactly the loopback observability socket and nothing else (SPEC §15: no
// listener other than obs on loopback).
func TestServeOnlyLoopbackListeners(t *testing.T) {
	e := newEnv(t)
	p := startServe(t, e, binPaths.main, nil)

	out, err := exec.CommandContext(context.Background(), "ss", "-ltnp").Output()
	if err != nil {
		t.Fatalf("ss -ltnp: %v (iproute2 is required on Linux CI)", err)
	}
	mark := fmt.Sprintf("pid=%d,", p.cmd.Process.Pid)
	listeners := 0
	for line := range strings.SplitSeq(string(out), "\n") {
		if !strings.Contains(line, mark) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			t.Fatalf("unparsable ss line: %q", line)
		}
		local := fields[3]
		host, _, ok := strings.Cut(local, "]:")
		if ok {
			host += "]"
		} else {
			host, _, ok = strings.Cut(local, ":")
			if !ok {
				t.Fatalf("unparsable local address %q", local)
			}
		}
		if host != "127.0.0.1" && host != "[::1]" {
			t.Errorf("serve listens on %q, want loopback only (line: %q)", local, line)
		}
		listeners++
	}
	if listeners != 1 {
		t.Fatalf("serve owns %d TCP listeners, want 1 (observability only)\n%s", listeners, out)
	}
}

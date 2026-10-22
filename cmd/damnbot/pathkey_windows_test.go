//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMonetaryKeyJunction: a junction into a payment skill resolves to the
// target's final path (SEC10-14); EvalSymlinks alone misses it.
func TestMonetaryKeyJunction(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	junction := filepath.Join(t.TempDir(), "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil { //nolint:gosec // fixed test command
		t.Skipf("mklink /J unavailable: %v (%s)", err, out)
	}
	if a, b := monetaryKey(junction), monetaryKey(target); a != b {
		t.Fatalf("junction %q != target %q", a, b)
	}
}

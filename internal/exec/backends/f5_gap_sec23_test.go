//go:build !minimal && unix

package backends

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// TestGAPF5_SEC23DockerProbe: a daemon that refuses the cgroup limit flags
// fails closed before the user command runs; a daemon that accepts them is
// probed exactly once. allow_unlimited is the only opt-out.
func TestGAPF5_SEC23DockerProbe(t *testing.T) {
	root := t.TempDir()
	bin, logPath, _ := fakeCLI(t, "fake-image")
	newBackend := func(unlimited bool) *Docker {
		d, err := NewDocker(&DockerConfig{
			Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: root,
			CPUMilli: 1000, MemoryMiB: 256, PIDs: 64, Network: "none", AllowUnlimited: unlimited,
		})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	t.Run("rejects", func(t *testing.T) {
		t.Setenv("FAKE_PROBE_FAIL", "1")
		d := newBackend(false)
		_, err := d.Run(context.Background(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: root, Timeout: 5 * time.Second})
		if !errors.Is(err, exec.ErrCapability) || !strings.Contains(err.Error(), "no_resource_limits") {
			t.Fatalf("Run = %v, want ErrCapability no_resource_limits", err)
		}
	})
	t.Run("accepts-and-caches", func(t *testing.T) {
		d := newBackend(false)
		for range 2 {
			if _, err := d.Run(context.Background(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: root, Timeout: 5 * time.Second}); err != nil {
				t.Fatalf("Run: %v", err)
			}
		}
		if n := strings.Count(readLog(t, logPath), "create"); n != 1 {
			t.Fatalf("probe create count = %d, want exactly 1", n)
		}
	})
	t.Run("allow-unlimited-skips", func(t *testing.T) {
		t.Setenv("FAKE_PROBE_FAIL", "1")
		if _, err := newBackend(true).Run(context.Background(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: root, Timeout: 5 * time.Second}); err != nil {
			t.Fatalf("allow_unlimited Run: %v", err)
		}
	})
}

// TestGAPF5_SEC23SingularityProbe: cgroup delegation is proven before the
// container starts; no delegation means ErrCapability no_resource_limits.
func TestGAPF5_SEC23SingularityProbe(t *testing.T) {
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	bin, _, _ := fakeCLI(t, "fake.sif")
	newBackend := func(unlimited bool) *Singularity {
		s, err := NewSingularity(&SingularityConfig{
			Binary: bin, Image: "fake.sif", Workspace: root,
			CPUMilli: 1000, MemoryMiB: 256, PIDs: 64, Network: "none", AllowUnlimited: unlimited,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	t.Run("rejects", func(t *testing.T) {
		t.Setenv("FAKE_PROBE_FAIL", "1")
		_, err := newBackend(false).Run(context.Background(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: root, Timeout: 5 * time.Second})
		if !errors.Is(err, exec.ErrCapability) || !strings.Contains(err.Error(), "no_resource_limits") {
			t.Fatalf("Run = %v, want ErrCapability no_resource_limits", err)
		}
	})
	t.Run("accepts", func(t *testing.T) {
		if _, err := newBackend(false).Run(context.Background(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: root, Timeout: 5 * time.Second}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	})
}

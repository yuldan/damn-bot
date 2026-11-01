//go:build !minimal && unix

package backends

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/netx"
)

// backendFixture is one implementation of the shared contract plus the
// command whose exit code and output it must reproduce.
type backendFixture struct {
	name string
	make func(t *testing.T) exec.Backend
	want string
	code int
}

// TestBackendContract runs one spawn/timeout/sweep contract per backend.
//
//nolint:gocognit,gocyclo // table of backend fixtures and shared assertions
func TestBackendContract(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dockerBin, _, _ := fakeCLI(t, "fake-image")
	singularityBin, _, _ := fakeCLI(t, "fake.sif")
	localRunner := exec.New(t.Context())
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		localRunner.CloseContext(context.Background())
	})
	api := &fakeSandbox{delay: 2 * time.Second}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	fixtures := []backendFixture{
		{"local", func(*testing.T) exec.Backend { return exec.NewLocal(localRunner) }, "local-ok", 5},
		{"docker", func(t *testing.T) exec.Backend {
			b, err := NewDocker(&DockerConfig{
				Binary: dockerBin, Image: "fake-image", User: "65534:65534",
				Workspace: root, CPUMilli: 1000, MemoryMiB: 256, PIDs: 64, Network: "none",
			})
			if err != nil {
				t.Fatalf("NewDocker: %v", err)
			}
			return b
		}, "docker-ok", 5},
		{"singularity", func(t *testing.T) exec.Backend {
			b, err := NewSingularity(&SingularityConfig{
				Binary: singularityBin, Image: "fake.sif",
				Workspace: root, CPUMilli: 1000, MemoryMiB: 256, PIDs: 64, Network: "none",
			})
			if err != nil {
				t.Fatalf("NewSingularity: %v", err)
			}
			return b
		}, "singularity-ok", 5},
		{"vercel_sandbox", func(t *testing.T) exec.Backend {
			net := netx.New(netx.Config{})
			t.Cleanup(net.CloseIdleConnections)
			b, err := NewSandbox(&SandboxConfig{
				Provider: "vercel_sandbox", Endpoint: srv.URL,
				Token: func(context.Context) (string, error) { return "t", nil }, Runtime: "node24",
				Timeout: time.Minute, Client: net.Client("exec-contract", netx.Exec),
			})
			if err != nil {
				t.Fatalf("NewSandbox: %v", err)
			}
			return b
		}, "from-sandbox", 3},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			b := f.make(t)
			spec := &exec.Spec{Argv: []string{"/bin/sh", "-c", "printf contract"}, Dir: root, Timeout: 5 * time.Second}
			if f.name == "local" || f.name == "docker" || f.name == "singularity" {
				spec.Argv = []string{"/bin/sh", "-c", "printf " + f.want + "; exit 5"}
			}
			res, err := b.Run(t.Context(), spec)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Status != exec.StatusExited || res.ExitCode == nil || *res.ExitCode != f.code {
				t.Fatalf("spawn = %+v", res)
			}
			if string(res.Output) != f.want {
				t.Fatalf("output = %q, want %q", res.Output, f.want)
			}
			res, err = b.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/sh", "-c", "sleep 20"}, Dir: root, Timeout: 150 * time.Millisecond})
			if err != nil || res.Status != exec.StatusTimeout {
				t.Fatalf("timeout = %+v, %v", res, err)
			}
			if n, serr := b.Sweep(t.Context(), res.ExecID); serr != nil || n < 0 {
				t.Fatalf("Sweep = %d, %v", n, serr)
			}
			if cerr := b.Close(t.Context()); cerr != nil {
				t.Fatalf("Close: %v", cerr)
			}
		})
	}
}

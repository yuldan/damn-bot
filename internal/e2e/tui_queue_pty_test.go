//go:build linux || darwin

package e2e

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// syncBuffer is the PTY output sink read by the test while the child writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, _ := b.b.Write(p)
	return n, nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// tuiProc is one live TUI child under a PTY.
type tuiProc struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	exitCh chan struct{}
	out    *syncBuffer
}

func (p *tuiProc) stop(t *testing.T) {
	t.Helper()
	if err := p.ptmx.Close(); err != nil {
		t.Logf("close pty: %v", err)
	}
	select {
	case <-p.exitCh:
	case <-time.After(2 * time.Second):
		if err := p.cmd.Process.Kill(); err != nil {
			t.Logf("kill tui: %v", err)
		}
		<-p.exitCh
	}
}

// TestTUIQueueUnderPTY runs the real binary (bench build) against the fake
// provider: typing a second prompt while the first reply is still streaming
// queues it, and only after that stream closes does the next provider request
// carry it. The queue must be visible in the live TUI before the first reply.
func TestTUIQueueUnderPTY(t *testing.T) {
	first := providertest.Text("first answer")
	first.Delay = time.Second
	e := New(t, first, providertest.Text("second answer"))
	writeTUIConfig(t, e)
	proc := startBenchTUI(t, e, buildBenchTUI(t))
	t.Cleanup(func() { proc.stop(t) })

	tuiWait(t, proc.out, "enter send", 15*time.Second, "prompt")
	queueScenario(t, proc, e.Server)
	if _, err := proc.ptmx.Write([]byte{3}); err != nil {
		t.Fatalf("ctrl+c: %v", err)
	}
	select {
	case <-proc.exitCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("TUI did not exit after ctrl+c; output:\n%s", proc.out.String())
	}
}

func writeTUIConfig(t *testing.T, e *Env) {
	t.Helper()
	cfgDir := filepath.Join(e.home, "damnbot")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[provider]\nmodel = \"openai/gpt-fake\"\n[network]\noffline = true\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func buildBenchTUI(t *testing.T) string {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "damnbot-bench")
	build := exec.CommandContext(t.Context(), "go", "build", "-tags", "goolm,bench", "-trimpath", "-o", bin, "./cmd/damnbot") // #nosec G204 -- fixed go tool, test-built output path
	build.Dir = root
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("build bench TUI: %v\n%s", berr, out)
	}
	return bin
}

func startBenchTUI(t *testing.T, e *Env, bin string) *tuiProc {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, "--plain") // #nosec G204 -- binary built by this test
	cmd.Dir = e.Workdir
	cmd.Env = append(os.Environ(),
		"HOME="+e.home,
		"XDG_CONFIG_HOME="+e.home,
		"XDG_DATA_HOME="+e.DataDir,
		"TERM=xterm-256color",
		"DAMNBOT_BENCH_MODEL=openai/gpt-fake",
		"DAMNBOT_BENCH_BASE_URL="+e.Server.URL,
		"DAMNBOT_BENCH_API_KEY=e2e",
	)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatalf("pty size: %v", err)
	}
	p := &tuiProc{cmd: cmd, ptmx: ptmx, exitCh: make(chan struct{}), out: &syncBuffer{}}
	go func() {
		if werr := cmd.Wait(); werr != nil {
			t.Logf("tui exit: %v", werr)
		}
		close(p.exitCh)
	}()
	go func() {
		if _, cerr := io.Copy(p.out, ptmx); cerr != nil {
			return
		}
	}()
	return p
}

func queueScenario(t *testing.T, p *tuiProc, srv *providertest.Server) {
	t.Helper()
	if _, err := p.ptmx.WriteString("first\r"); err != nil {
		t.Fatalf("type first: %v", err)
	}
	tuiWaitRequests(t, srv, 1, 10*time.Second)
	if _, err := p.ptmx.WriteString("second\r"); err != nil {
		t.Fatalf("type second: %v", err)
	}
	tuiWait(t, p.out, "queue 1", 10*time.Second, "queue indicator")
	if got := len(srv.Requests()); got != 1 {
		t.Fatalf("provider requests before first stream closes = %d, want 1 (queued prompt leaked)", got)
	}
	tuiWaitRequests(t, srv, 2, 20*time.Second)
	reqs := srv.Requests()
	if len(reqs) < 2 || !bytes.Contains(reqs[0], []byte("first")) || !bytes.Contains(reqs[1], []byte("second")) {
		t.Fatalf("provider request order lost queued prompt: %d requests", len(reqs))
	}
}

func tuiWait(t *testing.T, out *syncBuffer, substr string, d time.Duration, what string) {
	t.Helper()
	if !poll(d, func() bool { return strings.Contains(out.String(), substr) }) {
		t.Fatalf("%s not rendered within %s; output:\n%s", what, d, out.String())
	}
}

func tuiWaitRequests(t *testing.T, srv *providertest.Server, n int, d time.Duration) {
	t.Helper()
	if !poll(d, func() bool { return len(srv.Requests()) >= n }) {
		t.Fatalf("provider requests = %d, want at least %d", len(srv.Requests()), n)
	}
}

func poll(d time.Duration, cond func() bool) bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		if cond() {
			return true
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return false
		}
	}
}

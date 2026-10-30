package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fsProc is the real ACP server with pipes, so a client-fs scenario can
// answer the agent's outbound requests while a turn runs.
type fsProc struct {
	t    *testing.T
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	errb *lockedBuffer
	sid  string
	home string
}

// lockedBuffer is a process stderr buffer written by os/exec's copy
// goroutine and read by the test under -race.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.buf.Write(p); err != nil {
		return 0, fmt.Errorf("stderr buffer: %w", err)
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (p *fsProc) send(v any) {
	p.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		p.t.Fatalf("write frame: %v (stderr %s)", err, p.errb.String())
	}
}

func (p *fsProc) recv() map[string]any {
	p.t.Helper()
	line, err := p.out.ReadString('\n')
	if err != nil {
		p.t.Fatalf("read frame: %v (stderr %s)", err, p.errb.String())
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		p.t.Fatalf("frame %q: %v", line, err)
	}
	return v
}

// fsConfigDir mirrors internal/config baseDirs for the harness HOME.
func fsConfigDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "damnbot")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "damnbot")
	}
	return filepath.Join(home, "damnbot")
}

// fsChildEnv is os.Environ without proxy/TLS overrides plus the harness HOME
// and the scenario's extra entries.
func fsChildEnv(e *Env, extra []string) []string {
	drop := []string{"HTTPS_PROXY=", "https_proxy=", "HTTP_PROXY=", "http_proxy=", "NO_PROXY=", "no_proxy=", "SSL_CERT_FILE=", "SSL_CERT_DIR="}
	base := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		skip := false
		for _, p := range drop {
			if strings.HasPrefix(kv, p) {
				skip = true
				break
			}
		}
		if !skip {
			base = append(base, kv)
		}
	}
	base = append(base,
		"HOME="+e.home,
		"XDG_CONFIG_HOME="+e.home,
		"USERPROFILE="+e.home,
		"APPDATA="+filepath.Join(e.home, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(e.home, "AppData", "Local"),
	)
	return append(base, extra...)
}

// acpE2EBin builds the acpe2e binary (test-only provider base URL seam) for
// one scenario; building here keeps TestMain free of a second exit path.
func acpE2EBin(t *testing.T) string {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	name := "damnbot-acpe2e"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(t.TempDir(), name)
	build := exec.CommandContext(t.Context(), "go", "build", "-tags", "goolm,acpe2e", "-o", out, "./cmd/damnbot") // #nosec G204 -- fixed go tool
	build.Dir = root
	if b, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("build acpe2e: %v\n%s", berr, b)
	}
	return out
}

// startFSACP writes the isolated config and starts `damnbot acp` in Workdir.
func startFSACP(t *testing.T, e *Env, extraConfig string, extraEnv ...string) *fsProc {
	t.Helper()
	dir := fsConfigDir(e.home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(extraConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	cmd := exec.CommandContext(ctx, acpE2EBin(t), "acp") // #nosec G204 -- test-built binary
	cmd.Dir = e.Workdir
	cmd.Env = fsChildEnv(e, extraEnv)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	errb := &lockedBuffer{}
	cmd.Stderr = errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &fsProc{t: t, cmd: cmd, in: in, out: bufio.NewReader(out), errb: errb, home: e.home}
	t.Cleanup(func() {
		if werr := stopACP(cmd, in.Close, cancel); werr != nil {
			t.Errorf("wait: %v\nstderr:\n%s", werr, errb.String())
		}
	})
	return p
}

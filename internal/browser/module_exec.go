//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// browser_exec bounds (TOOL-019): the script size, the run time and the output.
const (
	maxExecScript  = 8192
	maxExecOutput  = 64 << 10
	execMinTimeout = time.Second
	execMaxTimeout = 2 * time.Minute
)

// stdioProcess is the optional host.Process extension a spawner returns when
// it owns pipes; without it browser_exec fails closed instead of guessing.
type stdioProcess interface {
	Stdio() (io.WriteCloser, io.ReadCloser, io.ReadCloser)
}

// execScript runs one Python helper through the Browser Use CLI (TOOL-019).
// The backend is only available when `[modules.browser].engine = "browser-use"`
// and `use_bin` names an absolute executable; the CLI reads one program from
// stdin and writes its result to stdout. The tool is non-read: approval shows
// only the script length and the program name.
func (m *Module) execScript(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		Script    string `json:"script"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	if a.Script == "" || len(a.Script) > maxExecScript {
		return host.Result{}, invalidArgs(fmt.Errorf("%w: script must be 1-%d bytes", errBadConfig, maxExecScript))
	}
	timeout := execDefaultTimeout
	if a.TimeoutMS != 0 {
		timeout = time.Duration(a.TimeoutMS) * time.Millisecond
	}
	if timeout < execMinTimeout || timeout > execMaxTimeout {
		return host.Result{}, invalidArgs(fmt.Errorf("%w: timeout_ms must be 1000-120000", errBadConfig))
	}
	if m.engineName() != "browser-use" {
		return host.Result{}, &host.Error{
			Code: "module_unavailable", Message: "browser_exec needs engine browser-use",
		}
	}
	if m.cfg.UseBin == "" || m.deps.Spawn == nil {
		return host.Result{}, &host.Error{Code: "module_unavailable", Message: "browser_exec has no use_bin or spawner"}
	}
	preview := fmt.Sprintf("browser_exec %d characters via %s", len(a.Script), filepath.Base(m.cfg.UseBin))
	if err := m.approve(ctx, "browser_exec", "browser:exec", preview, env, raw); err != nil {
		return host.Result{}, err
	}
	out, err := m.runBrowserUse(ctx, []byte(a.Script), timeout)
	if err != nil {
		return host.Result{}, err
	}
	content := string(out)
	if content == "" {
		content = "(no output)"
	}
	return host.Result{
		Content: content, Meta: map[string]any{"bytes": len(out), "engine": "browser-use"},
		UntrustedSource: "browser:browser-use",
	}, nil
}

// execDefaultTimeout is the run time of one browser_exec call.
const execDefaultTimeout = 60 * time.Second

// runBrowserUse spawns the CLI, writes the program on stdin and reads a bounded
// stdout. stderr is drained and discarded: it is model-invisible and may carry
// environment values.
func (m *Module) runBrowserUse(ctx context.Context, script []byte, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p, err := m.deps.Spawn(ctx, host.SpawnSpec{Path: m.cfg.UseBin})
	if err != nil {
		return nil, &host.Error{Code: "module_unavailable", Message: "browser_exec: " + err.Error()}
	}
	pipes, ok := p.(stdioProcess)
	if !ok {
		_ = p.Kill()
		return nil, &host.Error{Code: "module_unavailable", Message: "browser_exec: spawner has no stdio pipes"}
	}
	stdin, stdout, stderr := pipes.Stdio()
	if stdin == nil || stdout == nil {
		_ = p.Kill()
		return nil, &host.Error{Code: "module_unavailable", Message: "browser_exec: nil stdio pipes"}
	}
	if stderr != nil {
		go func() { _, _ = io.Copy(io.Discard, io.LimitReader(stderr, 4096)) }()
	}
	if _, werr := stdin.Write(script); werr != nil {
		_ = p.Kill()
		return nil, &host.Error{Code: "internal", Message: "browser_exec: stdin: " + werr.Error()}
	}
	if cerr := stdin.Close(); cerr != nil {
		_ = p.Kill()
		return nil, &host.Error{Code: "internal", Message: "browser_exec: stdin close: " + cerr.Error()}
	}
	data, rerr := io.ReadAll(io.LimitReader(stdout, maxExecOutput+1))
	if rerr != nil {
		_ = p.Kill()
		return nil, &host.Error{Code: "internal", Message: "browser_exec: read: " + rerr.Error()}
	}
	if len(data) > maxExecOutput {
		_ = p.Kill()
		return nil, &host.Error{Code: "too_large", Message: fmt.Sprintf("browser_exec: output %d > %d bytes", len(data), maxExecOutput)}
	}
	werr := p.Wait()
	if ctx.Err() != nil {
		return nil, &host.Error{Code: "timeout", Message: "browser_exec: timed out"}
	}
	if werr != nil {
		return nil, &host.Error{Code: "internal", Message: "browser_exec: exit: " + werr.Error()}
	}
	return data, nil
}

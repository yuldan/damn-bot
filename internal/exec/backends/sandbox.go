//go:build !minimal

package backends

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/netx"
)

// SandboxConfig is the composition-root view of an HTTP sandbox provider.
type SandboxConfig struct {
	Provider, Endpoint, Runtime, ProjectID string
	Token                                  func(context.Context) (string, error)
	Timeout                                time.Duration
	EnvAllow                               []string
	Client                                 *netx.Client
}

// Sandbox runs one command per call through a hosted control plane.
type Sandbox struct {
	cfg SandboxConfig
}

var _ exec.Backend = (*Sandbox)(nil)

// NewSandbox validates the provider and endpoint (P16: no request yet).
func NewSandbox(cfg *SandboxConfig) (*Sandbox, error) {
	if cfg.Token == nil || cfg.Client == nil {
		return nil, fmt.Errorf("%w: %s: network client and token secret are required", exec.ErrUnavailable, cfg.Provider)
	}
	defaults := map[string]string{"daytona": "https://app.daytona.io/api", "vercel_sandbox": "https://api.vercel.com"}
	def, known := defaults[cfg.Provider]
	if !known && cfg.Provider != "modal" {
		return nil, fmt.Errorf("%w: sandbox provider %q", exec.ErrSpec, cfg.Provider)
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = def
	}
	if cfg.Provider == "modal" && cfg.Endpoint == "" {
		return nil, fmt.Errorf("%w: modal needs exec.backends.modal.endpoint (gateway)", exec.ErrUnavailable)
	}
	if cfg.Provider == "vercel_sandbox" && cfg.Runtime == "" {
		return nil, fmt.Errorf("%w: vercel_sandbox runtime is required", exec.ErrSpec)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	return &Sandbox{cfg: *cfg}, nil
}

// ID is the provider id stored in config and journaled with every call.
func (s *Sandbox) ID() string { return s.cfg.Provider }

// Caps reports none: no shared filesystem, PTY, signal, limit or session.
func (s *Sandbox) Caps() exec.Caps { return exec.Caps{} }

// Run creates, execs and stops one sandbox.
func (s *Sandbox) Run(ctx context.Context, spec *exec.Spec) (res exec.Result, err error) {
	res = exec.Result{ExecID: exec.NewExecID(), Runner: s.ID(), Status: exec.StatusExited}
	if spec.PTY || spec.Limits.Set() {
		return exec.Result{Status: exec.StatusSpawnFailed}, fmt.Errorf("%w: %s: pty/resource_limits", exec.ErrCapability, s.ID())
	}
	env, err := EnvList(spec.Env, s.cfg.EnvAllow)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	timeout := spec.Timeout
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	token, terr := s.cfg.Token(callCtx)
	if terr != nil {
		res.Status = exec.StatusSpawnFailed
		return res, fmt.Errorf("%w: %s credential: %w", exec.ErrUnavailable, s.ID(), terr)
	}
	create := map[string]any{"name": res.ExecID}
	switch s.cfg.Provider {
	case "modal":
		create["timeout_s"], create["runtime"] = int(s.cfg.Timeout.Seconds()), s.cfg.Runtime
	case "daytona":
		create["ephemeral"] = true
	default:
		create["projectId"], create["runtime"] = s.cfg.ProjectID, s.cfg.Runtime
		create["timeout"] = int(s.cfg.Timeout.Milliseconds())
	}
	var ref sandboxReply
	if err := s.do(callCtx, token, http.MethodPost, s.url("create", ""), create, &ref); err != nil {
		res.Status = exec.StatusSpawnFailed
		return res, err
	}
	body := map[string]any{"env": envMap(env)}
	switch s.cfg.Provider {
	case "vercel_sandbox":
		body["command"], body["args"], body["wait"] = spec.Argv[0], spec.Argv[1:], true
	case "daytona":
		body["command"], body["timeout"] = shellLine(spec.Argv), int(timeout.Seconds())
	default:
		body["command"], body["timeout_s"] = shellLine(spec.Argv), int(timeout.Seconds())
	}
	var out sandboxReply
	xerr := s.do(callCtx, token, http.MethodPost, s.url("exec", ref.id()), body, &out)
	stopErr := s.do(context.WithoutCancel(ctx), token, sandboxAPIFor(s.cfg.Provider).method, s.url("stop", ref.id()), nil, nil)
	if xerr != nil {
		switch {
		case ctx.Err() != nil:
			res.Status = exec.StatusCancelledConfirmed
			return res, stopErr
		case callCtx.Err() != nil:
			res.Status = exec.StatusTimeout
			return res, stopErr
		}
		res.Status = exec.StatusSpawnFailed
		return res, errors.Join(xerr, stopErr)
	}
	code, ok := out.exitCode()
	if !ok {
		return res, errors.Join(fmt.Errorf("%w: %s exec returned no exit code", exec.ErrUnavailable, s.ID()), stopErr)
	}
	acc := &output{limit: exec.OutputCap}
	if _, werr := acc.Write([]byte(out.output())); werr != nil {
		return res, errors.Join(werr, stopErr)
	}
	res.Output, res.Truncated, res.BytesRaw = acc.result()
	res.ExitCode = &code
	return res, stopErr
}

type sandboxAPI struct{ create, exec, stop, method string }

func sandboxAPIFor(provider string) sandboxAPI {
	switch provider {
	case "modal":
		return sandboxAPI{"/v1/sandboxes", "/v1/sandboxes/%s/exec", "/v1/sandboxes/%s", http.MethodDelete}
	case "daytona":
		return sandboxAPI{"/sandbox", "/toolbox/%s/process/execute", "/sandbox/%s", http.MethodDelete}
	default:
		return sandboxAPI{"/v1/sandboxes", "/v1/sandboxes/%s/cmd", "/v1/sandboxes/%s/stop", http.MethodPost}
	}
}

func (s *Sandbox) url(op, sbx string) string {
	api := sandboxAPIFor(s.cfg.Provider)
	base, path := s.cfg.Endpoint, api.create
	switch op {
	case "exec":
		path = api.exec
	case "stop":
		path = api.stop
	}
	if op == "exec" && s.cfg.Provider == "daytona" {
		base = "https://proxy.app.daytona.io"
		if s.cfg.Endpoint != "https://app.daytona.io/api" {
			base = s.cfg.Endpoint
		}
	}
	path = strings.Replace(path, "%s", url.PathEscape(sbx), 1)
	return base + path
}

func (s *Sandbox) do(ctx context.Context, token, method, endpoint string, body, out any) (err error) {
	var rdr io.Reader
	if body != nil {
		raw, merr := json.Marshal(body)
		if merr != nil {
			return fmt.Errorf("%w: %s request: %w", exec.ErrSpec, s.ID(), merr)
		}
		rdr = bytes.NewReader(raw)
	}
	req, rerr := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if rerr != nil {
		return fmt.Errorf("%w: %s request: %w", exec.ErrUnavailable, s.ID(), rerr)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, derr := s.cfg.Client.DoOK(req)
	if derr != nil {
		return fmt.Errorf("%s %s: %w", s.ID(), method, derr)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("%w: %s body: %w", exec.ErrUnavailable, s.ID(), cerr))
		}
	}()
	raw, rerr2 := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if rerr2 != nil {
		return fmt.Errorf("%w: %s response: %w", exec.ErrUnavailable, s.ID(), rerr2)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if jerr := json.Unmarshal(raw, out); jerr != nil {
		return fmt.Errorf("%w: %s response: %w", exec.ErrUnavailable, s.ID(), jerr)
	}
	return nil
}

type sandboxReply struct {
	ID       string        `json:"id"`
	Sandbox  *sandboxReply `json:"sandbox"`
	ExitCode *int          `json:"exitCode"`
	Result   string        `json:"result"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	Command  *sandboxReply `json:"command"`
}

func (r *sandboxReply) id() string {
	if r.ID != "" {
		return r.ID
	}
	if r.Sandbox != nil {
		return r.Sandbox.ID
	}
	return ""
}

func (r *sandboxReply) exitCode() (int, bool) {
	if r.ExitCode != nil {
		return *r.ExitCode, true
	}
	if r.Command != nil {
		return r.Command.exitCode()
	}
	return 0, false
}

func (r *sandboxReply) output() string {
	text := r.Result + r.Stdout + r.Stderr
	if r.Command != nil {
		text += r.Command.output()
	}
	return text
}

func shellLine(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = ShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok {
			out[name] = value
		}
	}
	return out
}

// Sweep has nothing to kill: Run stops its sandbox; Timeout bounds a crash.
func (s *Sandbox) Sweep(context.Context, string) (int, error) { return 0, nil }

// Close has nothing to release: a sandbox only lives inside Run.
func (s *Sandbox) Close(context.Context) error { return nil }

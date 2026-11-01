package backends

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// CloudConfig is the composition-root view of one HTTP sandbox backend
// (modal, daytona, vercel_sandbox). Token is resolved from the secret store
// by the composition root and never enters a child environment.
type CloudConfig struct {
	Provider  string // modal | daytona | vercel_sandbox
	Endpoint  string
	Token     string
	Image     string
	Timeout   time.Duration
	ProjectID string
	TeamID    string
	EnvAllow  []string
	// Client is a test seam; nil uses a bounded http.Client.
	Client *http.Client
}

// Cloud runs commands in a remote sandbox over the provider's HTTP exec
// endpoint. It fails closed without endpoint+token; it never falls back to
// a local process.
type Cloud struct {
	cfg CloudConfig
	hc  *http.Client

	mu     sync.Mutex
	active map[string]bool
	closed bool
}

var _ exec.Backend = (*Cloud)(nil)

// cloudRequest is the adapter contract: argv/cwd/env/stdin in, combined
// output and exit code out. The token travels only in the Authorization
// header.
type cloudRequest struct {
	Argv      []string          `json:"argv"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Stdin     []byte            `json:"stdin,omitempty"`
	TimeoutMS int               `json:"timeout_ms"`
	Image     string            `json:"image,omitempty"`
}

type cloudResponse struct {
	ID        string `json:"id"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}

// NewCloud validates credentials and returns the backend.
func NewCloud(cfg *CloudConfig) (*Cloud, error) {
	switch cfg.Provider {
	case "modal", "daytona", "vercel_sandbox":
	default:
		return nil, fmt.Errorf("%w: cloud provider %q", exec.ErrSpec, cfg.Provider)
	}
	if cfg.Endpoint == "" || cfg.Token == "" {
		return nil, fmt.Errorf("%w: %s needs endpoint and token", exec.ErrUnavailable, cfg.Provider)
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Timeout == 0 {
		cfg.Timeout = exec.DefaultTimeout
	}
	hc := cfg.Client
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout + 5*time.Second}
	}
	return &Cloud{cfg: *cfg, hc: hc, active: map[string]bool{}}, nil
}

// ID is the provider id.
func (c *Cloud) ID() string { return c.cfg.Provider }

// Caps reports that the sandbox is remote; we cannot prove PTY, signals, resource limits
// or shared filesystem, so none is claimed.
func (c *Cloud) Caps() exec.Caps { return exec.Caps{} }

// Run posts one exec request and maps its answer into Result.
//
//nolint:gocyclo // the status mapping mirrors the provider error classes
func (c *Cloud) Run(ctx context.Context, spec *exec.Spec) (exec.Result, error) {
	res := exec.Result{ExecID: exec.NewExecID(), Runner: c.ID(), Status: exec.StatusExited}
	if spec.PTY {
		res.Status = exec.StatusSpawnFailed
		return res, fmt.Errorf("%w: %s: pty", exec.ErrCapability, c.ID())
	}
	if spec.Limits.Set() {
		res.Status = exec.StatusSpawnFailed
		return res, fmt.Errorf("%w: %s: resource_limits", exec.ErrCapability, c.ID())
	}
	env, err := envMap(spec.Env, c.cfg.EnvAllow)
	if err != nil {
		res.Status = exec.StatusSpawnFailed
		return res, err
	}
	if c.isClosed() {
		res.Status = exec.StatusSpawnFailed
		return res, fmt.Errorf("%w: %s backend closed", exec.ErrUnavailable, c.ID())
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = c.cfg.Timeout
	}
	body, err := json.Marshal(cloudRequest{
		Argv: spec.Argv, Cwd: spec.Dir, Env: env, Stdin: spec.Stdin,
		TimeoutMS: int(timeout / time.Millisecond), Image: c.cfg.Image,
	})
	if err != nil {
		res.Status = exec.StatusSpawnFailed
		return res, fmt.Errorf("%w: %s: encode: %w", exec.ErrUnavailable, c.ID(), err)
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.execURL(), bytes.NewReader(body))
	if err != nil {
		res.Status = exec.StatusSpawnFailed
		return res, fmt.Errorf("%w: %s: request: %w", exec.ErrUnavailable, c.ID(), err)
	}
	c.decorate(req)
	resp, err := c.hc.Do(req)
	res.Duration = time.Since(start)
	if err != nil {
		res.Status = exec.StatusSpawnFailed
		if errors.Is(err, context.DeadlineExceeded) || callCtx.Err() != nil {
			res.Status = exec.StatusTimeout
			if ctx.Err() != nil {
				res.Status = exec.StatusCancelledConfirmed
			}
		}
		return res, fmt.Errorf("%w: %s: %w", exec.ErrUnavailable, c.ID(), err)
	}
	defer closeBody(resp)
	out, err := decodeCloud(resp, c.ID())
	if err != nil {
		res.Status = exec.StatusSpawnFailed
		return res, err
	}
	if out.ID != "" {
		c.track(out.ID)
	}
	combined := out.Stdout + out.Stderr
	res.Output = capOutput([]byte(combined))
	res.Truncated = out.Truncated || len(combined) > exec.OutputCap
	res.BytesRaw = int64(len(combined))
	res.ExitCode = &out.ExitCode
	res.Status = exec.StatusExited
	if spec.OnOutput != nil && len(res.Output) > 0 {
		spec.OnOutput(res.Output)
	}
	return res, nil
}

// decodeCloud reads one response body and validates the status.
func decodeCloud(resp *http.Response, id string) (cloudResponse, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, exec.OutputCap+1))
	if err != nil {
		return cloudResponse{}, fmt.Errorf("%w: %s: read: %w", exec.ErrUnavailable, id, err)
	}
	if resp.StatusCode != http.StatusOK {
		return cloudResponse{}, fmt.Errorf("%w: %s: http %d", exec.ErrUnavailable, id, resp.StatusCode)
	}
	var out cloudResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return cloudResponse{}, fmt.Errorf("%w: %s: decode: %w", exec.ErrUnavailable, id, err)
	}
	return out, nil
}

// closeBody drains a response body; a close error after a read is not
// actionable.
func closeBody(resp *http.Response) {
	//nolint:errcheck,gosec // the body content was already consumed
	resp.Body.Close()
}

// execURL is the provider exec endpoint.
func (c *Cloud) execURL() string {
	switch c.cfg.Provider {
	case "daytona":
		return c.cfg.Endpoint + "/toolbox/exec"
	case "vercel_sandbox":
		return c.cfg.Endpoint + "/v1/sandboxes/exec"
	default:
		return c.cfg.Endpoint + "/v1/exec"
	}
}

// decorate adds the bearer token (and provider ids); nothing secret goes
// into argv, env, or the request body.
func (c *Cloud) decorate(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.ProjectID != "" {
		req.Header.Set("X-Project-Id", c.cfg.ProjectID)
	}
	if c.cfg.TeamID != "" {
		req.Header.Set("X-Team-Id", c.cfg.TeamID)
	}
}

func (c *Cloud) track(id string) {
	c.mu.Lock()
	c.active[id] = true
	c.mu.Unlock()
}

func (c *Cloud) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Sweep terminates a sandbox by id (best effort DELETE).
func (c *Cloud) Sweep(ctx context.Context, execID string) (int, error) {
	if execID == "" {
		return 0, fmt.Errorf("%w: empty exec id", exec.ErrSpec)
	}
	if err := c.terminate(ctx, execID); err != nil {
		return 0, err
	}
	c.mu.Lock()
	delete(c.active, execID)
	c.mu.Unlock()
	return 1, nil
}

// Close terminates every sandbox this backend started.
func (c *Cloud) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	ids := make([]string, 0, len(c.active))
	for id := range c.active {
		ids = append(ids, id)
	}
	c.active = map[string]bool{}
	c.mu.Unlock()
	var errs error
	for _, id := range ids {
		errs = errors.Join(errs, c.terminate(ctx, id))
	}
	return errs
}

func (c *Cloud) terminate(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.cfg.Endpoint+"/v1/sandboxes/"+id, nil)
	if err != nil {
		return fmt.Errorf("%w: %s terminate: %w", exec.ErrUnavailable, c.ID(), err)
	}
	c.decorate(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s terminate: %w", exec.ErrUnavailable, c.ID(), err)
	}
	closeBody(resp)
	return nil
}

// envMap converts the child environment into the request map, refusing
// secret-like names not explicitly allowed. The provider token is never
// included.
func envMap(env []string, allow []string) (map[string]string, error) {
	list, err := envList(env, allow)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(list))
	for _, kv := range list {
		name, value, _ := strings.Cut(kv, "=")
		out[name] = value
	}
	return out, nil
}

// capOutput applies the head+tail OutputCap rule to a completed body.
func capOutput(data []byte) []byte {
	if len(data) <= exec.OutputCap {
		return data
	}
	half := exec.OutputCap / 2
	return append(append([]byte(nil), data[:half]...), data[len(data)-half:]...)
}

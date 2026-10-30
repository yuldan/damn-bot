// Package hooks runs executable hook extensions (SPEC §4.10, ADR-009).
package hooks

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/redact"
)

// Version is the stdin version field (CROSS-CUTTING §8).
const Version = "damnbot.hook/v1"

// DefaultTimeout is the SPEC §4.10 hook timeout.
const DefaultTimeout = 5 * time.Second

// DefaultMaxOutput caps each captured stream (fixed; not a config key).
const DefaultMaxOutput = 64 << 10

// Event is one hook event of the SPEC §4.10 table.
type Event string

// Hook events (SPEC §4.10).
const (
	EventSessionStart   Event = "session_start"
	EventTurnStart      Event = "turn_start"
	EventPreTool        Event = "pre_tool"
	EventPostTool       Event = "post_tool"
	EventTurnEnd        Event = "turn_end"
	EventGatewayInbound Event = "gateway_inbound"
	EventPreVerify      Event = "pre_verify"
)

// Decision is a hook response decision.
type Decision string

// Decisions (SPEC §4.10).
const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
	DecisionAsk   Decision = "ask"
)

// Closed errors (errors.Is).
var (
	ErrInvalidName    = errors.New("hooks: invalid hook name")
	ErrNotFound       = errors.New("hooks: hook not found")
	ErrNotExecutable  = errors.New("hooks: hook is not an executable regular file")
	ErrTimeout        = errors.New("hooks: hook timed out")
	ErrExit           = errors.New("hooks: hook exited non-zero")
	ErrResponse       = errors.New("hooks: invalid hook response")
	ErrInvalidEvent   = errors.New("hooks: invalid hook event")
	ErrNotHookProgram = errors.New("hooks: cannot execute hook")
)

// Hook is a discovered file; Problem explains why it cannot run ("" = ok).
type Hook struct {
	Name    string
	Problem string
}

// Result is one hook run. Output is already capped and redacted.
type Result struct {
	Name, Stdout, Stderr, Reason string
	Event                        Event
	Decision                     Decision
	ExitCode                     int
	Truncated                    bool
	Duration                     time.Duration
}

// Verdict aggregates hooks in name order: first deny/error wins.
type Verdict struct {
	Decision     Decision
	Hook, Reason string
	Results      []Result
}

// Registry is one hooks directory; construct it with New.
type Registry struct {
	dir       string
	timeout   time.Duration
	maxOutput int
	env       []string // parent environment; nil = os.Environ()
	red       *redact.Redactor
}

// New returns a registry for dir (a missing dir is empty).
func New(dir string) *Registry {
	return &Registry{
		dir:       dir,
		timeout:   DefaultTimeout,
		maxOutput: DefaultMaxOutput,
		red:       redact.New(redact.Options{Secrets: redact.SecretsFromEnv(os.Environ())}),
	}
}

// List returns every entry in name order, runnable or not.
func (r *Registry) List() ([]Hook, error) {
	entries, err := os.ReadDir(r.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hooks: read %s: %w", r.dir, err)
	}
	out := make([]Hook, 0, len(entries))
	for _, e := range entries {
		h := Hook{Name: e.Name(), Problem: r.problem(e.Name(), e)}
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b Hook) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Lookup validates name; it can never leave the directory.
func (r *Registry) Lookup(name string) (Hook, error) {
	if !validName(name) {
		return Hook{Name: name}, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	path := filepath.Join(r.dir, name)
	e, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Hook{Name: name}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	if err != nil {
		return Hook{Name: name}, fmt.Errorf("hooks: stat %s: %w", path, err)
	}
	if problem := r.problem(name, fs.FileInfoToDirEntry(e)); problem != "" {
		return Hook{Name: name, Problem: problem}, fmt.Errorf("%w: %s: %s", ErrNotExecutable, name, problem)
	}
	return Hook{Name: name}, nil
}

// Fire runs every runnable hook in name order; a deny or error ends it.
func (r *Registry) Fire(ctx context.Context, ev Event, fields map[string]any) (Verdict, error) {
	if !ev.valid() {
		return Verdict{}, fmt.Errorf("%w: %q", ErrInvalidEvent, ev)
	}
	all, err := r.List()
	if err != nil {
		return Verdict{}, err
	}
	v := Verdict{Decision: DecisionAllow}
	for _, h := range all {
		if h.Problem != "" {
			continue
		}
		v.Hook = h.Name
		res, err := r.Run(ctx, h.Name, ev, fields)
		v.Results = append(v.Results, res)
		if err != nil {
			v.Reason = r.red.String(err.Error())
			return v, err
		}
		if res.Decision == DecisionDeny {
			v.Decision, v.Reason = DecisionDeny, res.Reason
			return v, nil
		}
	}
	v.Hook = ""
	return v, nil
}

// Run executes one named hook; Result is returned even on failure.
func (r *Registry) Run(ctx context.Context, name string, ev Event, fields map[string]any) (Result, error) {
	if !ev.valid() {
		return Result{Name: name, Event: ev}, fmt.Errorf("%w: %q", ErrInvalidEvent, ev)
	}
	hook, err := r.Lookup(name)
	if err != nil {
		return Result{Name: name, Event: ev}, err
	}
	timeout := cmp.Or(r.timeout, DefaultTimeout)
	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	max := cmp.Or(r.maxOutput, DefaultMaxOutput)
	stdout, stderr := &capWriter{max: max}, &capWriter{max: max}
	cmd := osexec.CommandContext(childCtx, filepath.Join(r.dir, hook.Name), string(ev)) // #nosec G204 -- validated name under r.dir
	cmd.Stdin = bytes.NewReader(payload(ev, fields))
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = r.childEnv()
	cmd.WaitDelay = time.Second // grandchildren holding a pipe cannot hang Run
	start := time.Now()
	runErr := cmd.Run()
	res := Result{
		Name: hook.Name, Event: ev, ExitCode: -1, Duration: time.Since(start),
		Stdout: r.red.String(stdout.String()), Stderr: r.red.String(stderr.String()),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if childCtx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("%w: %s after %s", ErrTimeout, name, timeout)
	}
	if ctx.Err() != nil {
		return res, fmt.Errorf("hooks: %s: %w", name, ctx.Err())
	}
	if runErr != nil {
		if exit, ok := errors.AsType[*osexec.ExitError](runErr); ok {
			res.ExitCode = exit.ExitCode()
			return res, fmt.Errorf("%w: %s exit %d", ErrExit, name, res.ExitCode)
		}
		return res, fmt.Errorf("%w: %s: %w", ErrNotHookProgram, name, runErr)
	}
	decision, reason, err := parseResponse(stdout.buf.Bytes())
	res.Decision, res.Reason = decision, r.red.String(reason)
	res.ExitCode = 0
	if err != nil {
		return res, err
	}
	if res.Decision == "" {
		res.Decision = DecisionAllow
	}
	return res, nil
}

func (ev Event) valid() bool {
	switch ev {
	case EventSessionStart, EventTurnStart, EventPreTool, EventPostTool, EventTurnEnd, EventGatewayInbound, EventPreVerify:
		return true
	}
	return false
}

// payload builds stdin; fields cannot override the reserved keys.
func payload(ev Event, fields map[string]any) []byte {
	m := maps.Clone(fields)
	if m == nil {
		m = make(map[string]any, 2)
	}
	m["version"], m["event"] = Version, string(ev)
	b, err := json.Marshal(m)
	if err != nil { // fields are JSON-supported values; defensive
		return []byte(`{"version":"` + Version + `","event":"` + string(ev) + `"}`)
	}
	return b
}

func parseResponse(out []byte) (Decision, string, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return DecisionAllow, "", nil
	}
	var resp struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", "", fmt.Errorf("%w: not a JSON object", ErrResponse)
	}
	switch d := Decision(resp.Decision); d {
	case "":
		return DecisionAllow, resp.Reason, nil
	case DecisionAllow, DecisionDeny, DecisionAsk:
		return d, resp.Reason, nil
	default:
		return "", "", fmt.Errorf("%w: unknown decision %q", ErrResponse, resp.Decision)
	}
}

// childEnv is the GAP-SEC-02 allowlist (no exec.env/_allow).
func (r *Registry) childEnv() []string {
	parent := r.env
	if parent == nil {
		parent = os.Environ()
	}
	return (&exec.EnvPolicy{}).Env(parent, false, "", "", "")
}

func (r *Registry) problem(name string, e fs.DirEntry) string {
	if !validName(name) {
		return "invalid name"
	}
	if e.Type()&fs.ModeSymlink != 0 {
		return "symlink"
	}
	info, err := e.Info()
	if errors.Is(err, fs.ErrNotExist) {
		return "vanished"
	}
	if err != nil {
		return "unreadable"
	}
	if !info.Mode().IsRegular() {
		return "not a regular file"
	}
	if !executable(name, info.Mode()) {
		return "not executable"
	}
	return ""
}

// executable: POSIX execute bit; on Windows only an executable extension.
func executable(name string, mode fs.FileMode) bool {
	if runtime.GOOS != "windows" {
		return mode.Perm()&0o111 != 0
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".exe", ".com", ".bat", ".cmd":
		return true
	}
	return false
}

// validName: 1..64 chars, alphanumeric first, then [A-Za-z0-9._-]; no dot first.
func validName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// capWriter keeps the first max bytes but drains the rest.
type capWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	n := len(p)
	if room := w.max - w.buf.Len(); room > 0 {
		part := p
		if len(part) > room {
			part = part[:room]
			w.truncated = true
		}
		w.buf.Write(part)
	} else if n > 0 {
		w.truncated = true
	}
	return n, nil
}

func (w *capWriter) String() string { return w.buf.String() }

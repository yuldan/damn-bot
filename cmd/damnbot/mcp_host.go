//go:build !minimal

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/mcp"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// mcpCLIHost is the composition root's host for non-turn MCP commands
// (`mcp list|refresh|review`). It owns process spawning, the namespaced
// module store over ctxdb.module_state, a terminal approval channel, and a
// deny-by-default budget/sampling surface. Nothing here is a second module
// host: the daemon keeps its own instance.
type mcpCLIHost struct {
	db      *ctxdb.Store
	approve host.Approvals
	secrets host.Secrets
	clock   host.Clock
	errOut  io.Writer
}

// Capabilities implements host.CapabilityProvider. Budget stays the
// fail-closed default: the CLI never spends tokens for sampling.
func (h *mcpCLIHost) Capabilities() host.Capabilities {
	return host.Capabilities{Approvals: h.approve, Secrets: h.secrets, Clock: h.clock}
}

// Emit reports module faults on stderr; a non-turn command has no journal.
func (h *mcpCLIHost) Emit(_ context.Context, ev host.Event) error {
	if h.errOut != nil && ev.Kind == event.ModuleError {
		sessionsPrintf(h.errOut, "mcp: %s\n", cleanLine(ev.Content))
	}
	return nil
}

// I18n returns nil: user-facing strings of this command are plain Go text.
func (*mcpCLIHost) I18n() host.Translator { return nil }

// LLM returns nil: sampling is not offered by a non-turn command.
func (*mcpCLIHost) LLM(string) host.Completer { return nil }

// Storage opens the module's ctxdb namespace.
func (h *mcpCLIHost) Storage(namespace string) (host.Store, error) {
	if h.db == nil {
		return nil, errors.New("mcp: no database")
	}
	if namespace == "" {
		return nil, errors.New("mcp: empty storage namespace")
	}
	return moduleStore{db: h.db, module: strings.TrimPrefix(namespace, "module/")}, nil
}

// Spawn runs a stdio MCP server with the environment the Secrets seam built.
func (h *mcpCLIHost) Spawn(ctx context.Context, spec host.SpawnSpec) (host.Process, error) {
	if spec.Path == "" {
		return nil, errors.New("mcp: empty spawn path")
	}
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...) //nolint:gosec // G204: path/argv come from validated [modules.mcp] config
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	errp, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %s: %w", spec.Path, err)
	}
	return &cliProcess{cmd: cmd, in: in, out: out, errp: errp, done: make(chan struct{})}, nil
}

// cliProcess reaps the child once and exposes its protocol pipes through the
// optional stdioPipes extension the MCP stdio transport requires.
type cliProcess struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	errp io.ReadCloser
	done chan struct{}
	mu   sync.Mutex
	err  error
	once sync.Once
}

func (p *cliProcess) startWait() {
	p.once.Do(func() {
		go func() {
			err := p.cmd.Wait()
			p.mu.Lock()
			p.err = err
			p.mu.Unlock()
			close(p.done)
		}()
	})
}

func (p *cliProcess) Stdio() (io.WriteCloser, io.Reader, io.Reader) {
	p.startWait()
	return p.in, p.out, p.errp
}

func (p *cliProcess) Done() <-chan struct{} {
	p.startWait()
	return p.done
}

func (p *cliProcess) Wait() error {
	p.startWait()
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *cliProcess) Signal(sig os.Signal) error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Signal(sig); err != nil {
		return fmt.Errorf("mcp: signal: %w", err)
	}
	return nil
}

func (p *cliProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("mcp: kill: %w", err)
	}
	return nil
}

// moduleStore is the host.Store seam over module_state: keys are opaque bytes
// scoped to one module id, values are replaced atomically per row.
type moduleStore struct {
	db     *ctxdb.Store
	module string
}

func (s moduleStore) Get(ctx context.Context, key []byte) ([]byte, bool, error) {
	var value string
	found := false
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		err := tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
			[]any{s.module, string(key)}, &value)
		switch {
		case errors.Is(err, ctxdb.ErrNotFound):
			return nil
		case err != nil:
			return err
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return nil, false, err
	}
	return []byte(value), true, nil
}

func (s moduleStore) Put(ctx context.Context, key, value []byte) error {
	return s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, err := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES (?1, ?2, ?3, ?4)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			s.module, string(key), string(value), time.Now().UnixMilli())
		return err
	})
}

func (s moduleStore) Delete(ctx context.Context, key []byte) error {
	return s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, err := tx.Exec(`DELETE FROM module_state WHERE module_id = ?1 AND key = ?2`, s.module, string(key))
		return err
	})
}

func (s moduleStore) List(ctx context.Context, prefix []byte) ([][]byte, error) {
	var values []string
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		out, err := tx.Strings(`SELECT value FROM module_state WHERE module_id = ?1 AND substr(key, 1, ?2) = ?3`,
			s.module, len(prefix), string(prefix))
		values = out
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(values))
	for _, v := range values {
		out = append(out, []byte(v))
	}
	return out, nil
}

// terminalApprovals is the one interactive consent channel of the CLI. It
// renders the preview and denies on EOF, a closed stdin, or anything but an
// explicit yes; a non-interactive caller therefore never approves.
type terminalApprovals struct {
	in  io.Reader
	out io.Writer
}

func (a *terminalApprovals) Approve(ctx context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	if err := req.Validate(); err != nil {
		return host.ApprovalDeny, err
	}
	sessionsPrintf(a.out, "%s (%s):\n%s\napprove? [y/N] ", req.Tool, req.Class, cleanLine(req.Preview))
	line, err := bufio.NewReader(a.in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return host.ApprovalDeny, fmt.Errorf("mcp: approval prompt: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return host.ApprovalAllowOnce, nil
	}
	return host.ApprovalDeny, nil
}

// envSecrets is the allowlist policy of ADR-268: only exact inherited names
// are copied, Set wins, and no other variable is visible.
type envSecrets struct{}

func (envSecrets) ChildEnv(policy host.AllowPolicy) ([]string, error) {
	out := make([]string, 0, len(policy.Inherit)+len(policy.Set))
	set := make(map[string]bool, len(policy.Set))
	for name, value := range policy.Set {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, host.ErrInvalidEnv
		}
		set[name] = true
		out = append(out, name+"="+value)
	}
	for _, name := range policy.Inherit {
		if name == "" || set[name] || strings.ContainsAny(name, "=\x00") {
			continue
		}
		if value, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+value)
		}
	}
	sort.Strings(out)
	return out, nil
}

// mcpWallClock is the CLI clock; expiry decisions are made on real wall time.
type mcpWallClock struct{}

func (mcpWallClock) Now() (time.Time, error) { return time.Now(), nil }

// cliMCPHost builds the host used by mcp list/refresh/review.
func cliMCPHost(db *ctxdb.Store, in io.Reader, out, errOut io.Writer) *mcpCLIHost {
	return &mcpCLIHost{db: db, approve: &terminalApprovals{in: in, out: out}, secrets: envSecrets{}, clock: mcpWallClock{}, errOut: errOut}
}

// newMCPClient builds one validated client for a configured server. The OAuth
// token source is created only for the oauth section; stdio servers never get
// one (mcp.NewClient enforces the same exclusivity).
func newMCPClient(h *mcpCLIHost, srv mcp.Server, cfg *config.Config) (*mcp.Client, error) {
	doer := mcpDoer()
	var opts []mcp.Option
	if srv.OAuth != nil {
		ref := ""
		if section, ok := cfg.Modules.MCP.Servers[srv.Name]; ok && section.OAuth != nil {
			ref = section.OAuth.ClientSecret
		}
		tokens, err := openMCPTokens(context.Background(), srv, ref, false, io.Discard)
		if err != nil {
			return nil, err
		}
		opts = append(opts, mcp.WithTokenSource(tokens))
	}
	pins, err := pinStoreFor(h.db)
	if err != nil {
		return nil, err
	}
	return mcp.NewClient(h, srv, doer, pins, opts...)
}

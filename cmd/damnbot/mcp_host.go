//go:build !minimal

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
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
	"github.com/damnbot/damnbot/internal/netx"
)

// mcpCLIHost is the composition root's host for non-turn MCP commands.
type mcpCLIHost struct {
	db      *ctxdb.Store
	approve host.Approvals
	secrets host.Secrets
	clock   host.Clock
	errOut  io.Writer
}

// Capabilities is fail-closed: the CLI never spends tokens for sampling.
func (h *mcpCLIHost) Capabilities() host.Capabilities {
	return host.Capabilities{Approvals: h.approve, Secrets: h.secrets, Clock: h.clock}
}

func (h *mcpCLIHost) Emit(_ context.Context, ev host.Event) error {
	if h.errOut != nil && ev.Kind == event.ModuleError {
		sessionsPrintf(h.errOut, "mcp: %s\n", cleanLine(ev.Content))
	}
	return nil
}

// I18n returns nil: user-facing strings of this command are plain Go text.
func (*mcpCLIHost) I18n() host.Translator { return nil }

func (*mcpCLIHost) LLM(string) host.Completer { return nil }

func (h *mcpCLIHost) Storage(namespace string) (host.Store, error) {
	if h.db == nil {
		return nil, errors.New("mcp: no database")
	}
	if namespace == "" {
		return nil, errors.New("mcp: empty storage namespace")
	}
	return moduleStore{db: h.db, module: strings.TrimPrefix(namespace, "module/")}, nil
}

func (h *mcpCLIHost) Spawn(ctx context.Context, spec host.SpawnSpec) (host.Process, error) { //nolint:gocritic // one spawn spec copied once
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

// cliProcess reaps the child and exposes its stdio pipes.
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

// moduleStore is the host.Store seam over module_state.
type moduleStore struct {
	db     *ctxdb.Store
	module string
}

func (s moduleStore) Get(ctx context.Context, key []byte) ([]byte, bool, error) {
	var value string
	found := false
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		terr := tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
			[]any{s.module, string(key)}, &value)
		switch {
		case errors.Is(terr, ctxdb.ErrNotFound):
			return nil
		case terr != nil:
			return fmt.Errorf("mcp: read module state: %w", terr)
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return nil, false, fmt.Errorf("mcp: read module state: %w", err)
	}
	return []byte(value), true, nil
}

func (s moduleStore) exec(ctx context.Context, query string, args ...any) error {
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, terr := tx.Exec(query, args...); terr != nil {
			return fmt.Errorf("mcp: module state: %w", terr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("mcp: module state: %w", err)
	}
	return nil
}

func (s moduleStore) Put(ctx context.Context, key, value []byte) error {
	return s.exec(ctx, `INSERT INTO module_state(module_id, key, value, updated_at) VALUES (?1, ?2, ?3, ?4)
		ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		s.module, string(key), string(value), time.Now().UnixMilli())
}

func (s moduleStore) Delete(ctx context.Context, key []byte) error {
	return s.exec(ctx, `DELETE FROM module_state WHERE module_id = ?1 AND key = ?2`, s.module, string(key))
}

func (s moduleStore) List(ctx context.Context, prefix []byte) ([][]byte, error) {
	var values []string
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		out, terr := tx.Strings(`SELECT value FROM module_state WHERE module_id = ?1 AND substr(key, 1, ?2) = ?3`,
			s.module, len(prefix), string(prefix))
		values = out
		if terr != nil {
			return fmt.Errorf("mcp: list module state: %w", terr)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: list module state: %w", err)
	}
	out := make([][]byte, 0, len(values))
	for _, v := range values {
		out = append(out, []byte(v))
	}
	return out, nil
}

// terminalApprovals denies everything but an explicit yes on an open stdin.
type terminalApprovals struct {
	in  io.Reader
	out io.Writer
}

func (a *terminalApprovals) Approve(ctx context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	if err := req.Validate(); err != nil {
		return host.ApprovalDeny, fmt.Errorf("mcp: approval request: %w", err)
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

// envSecrets copies only exact allowlisted names (ADR-268).
type envSecrets struct{ getenv func(string) string }

func (a envSecrets) ChildEnv(policy host.AllowPolicy) ([]string, error) {
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
		if value := a.getenv(name); value != "" {
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
func cliMCPHost(db *ctxdb.Store, loader *config.Loader, in io.Reader, out, errOut io.Writer) *mcpCLIHost {
	return &mcpCLIHost{db: db, approve: &terminalApprovals{in: in, out: out}, secrets: envSecrets{getenv: loader.Env}, clock: mcpWallClock{}, errOut: errOut}
}

// mcpDoerFor keeps loopback on the inference edge and public on the tool edge.
func mcpDoerFor(srv mcp.Server) *netx.Client { //nolint:gocritic // one validated server converted once
	if u, err := url.Parse(srv.URL); err == nil && isLoopbackHost(u.Hostname()) {
		return netx.New(netx.Config{NoBackground: true}).Client("mcp-local", netx.Inference)
	}
	return netx.New(netx.Config{NoBackground: true}).Client("mcp", netx.Tool)
}

// isLoopbackHost reports whether host is a loopback literal or localhost.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newMCPClient builds one validated client; OAuth is wired only for `oauth`.
func newMCPClient(h *mcpCLIHost, srv mcp.Server, cfg *config.Config) (*mcp.Client, error) { //nolint:gocritic // one validated server converted once
	doer := mcpDoerFor(srv)
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
	client, err := mcp.NewClient(h, srv, doer, pins, opts...)
	if err != nil {
		return nil, fmt.Errorf("mcp: client %s: %w", srv.Name, err)
	}
	return client, nil
}

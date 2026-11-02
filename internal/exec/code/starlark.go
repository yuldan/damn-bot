//go:build !minimal

package code

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// toolAllowlist is the seven RPC tools an execute_code script may call
// (EXEC-AND-CODING §B.7). Every other name is invalid_args, never forwarded.
func toolAllowlist() []string {
	return []string{"read_file", "write_file", "edit_file", "glob", "grep", "web_fetch", "shell"}
}

type rpcRequest struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type rpcResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

// startToolServer listens on a private Unix socket in dir for the duration of
// one Starlark call. It returns the socket path and a stop function.
func (r *Runner) startToolServer(ctx context.Context, dir, network string) (string, func(), error) {
	path, cleanup, err := socketPath(dir)
	if err != nil {
		return "", nil, err
	}
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%w: tool socket: %w", ErrBackend, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go r.serveToolConn(ctx, conn, network)
		}
	}()
	stop := func() {
		if cerr := ln.Close(); cerr != nil {
			return
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		cleanup()
	}
	return path, stop, nil
}

// socketPath keeps the Unix socket under the platform sun_path limit (~104 on
// macOS). A long data/work directory would otherwise make bind fail with
// "invalid argument"; fall back to a short 0700 temp dir.
func socketPath(dir string) (string, func(), error) {
	path := filepath.Join(dir, "tool.sock")
	if len(path) <= 96 {
		return path, func() {}, nil
	}
	short, err := os.MkdirTemp("", "damnbot-ec-")
	if err != nil {
		return "", nil, fmt.Errorf("%w: tool socket dir: %w", ErrBackend, err)
	}
	cleanup := func() {
		if err := os.RemoveAll(short); err != nil {
			return
		}
	}
	return filepath.Join(short, "t.sock"), cleanup, nil
}

func (r *Runner) serveToolConn(ctx context.Context, conn net.Conn, network string) {
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			return
		}
	}()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			writeRPC(conn, rpcResponse{Error: "invalid_args", Reason: "request is not JSON"})
			continue
		}
		writeRPC(conn, r.callTool(ctx, network, req))
	}
}

func (r *Runner) callTool(ctx context.Context, network string, req rpcRequest) rpcResponse {
	if !slices.Contains(toolAllowlist(), req.Name) {
		return rpcResponse{Error: "invalid_args", Reason: "tool is not in the RPC allowlist"}
	}
	// network=deny covers RPC (ADR-076). The local backend cannot isolate the
	// tool process, so the two network-capable tools are refused before the
	// pipeline sees them.
	if network == "deny" && (req.Name == "web_fetch" || req.Name == "shell") {
		return rpcResponse{Error: "denied", Reason: "network_deny"}
	}
	if r.cfg.Tool == nil {
		return rpcResponse{Error: "module_unavailable", Reason: "tool pipeline is not wired"}
	}
	if len(req.Args) == 0 {
		req.Args = json.RawMessage(`{}`)
	}
	out, err := r.cfg.Tool.Tool(ctx, req.Name, req.Args)
	if err != nil {
		return rpcResponse{Error: "exec_failed", Reason: err.Error()}
	}
	if !json.Valid(out) {
		return rpcResponse{Error: "internal", Reason: "tool result is not JSON"}
	}
	return rpcResponse{OK: true, Result: out}
}

func writeRPC(conn net.Conn, resp rpcResponse) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return
	}
	if _, werr := conn.Write(append(raw, '\n')); werr != nil {
		return
	}
}

// CallToolRPC is the child-side client of one tool call over the private
// socket. It is shared with the __starlark command.
func CallToolRPC(path, name string, args map[string]any) (json.RawMessage, error) {
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := netx.New(netx.Config{}).DialUnix(dialCtx, path)
	if err != nil {
		return nil, fmt.Errorf("tool socket: %w", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			return
		}
	}()
	body, err := json.Marshal(rpcRequest{Name: name, Args: mustJSON(args)})
	if err != nil {
		return nil, fmt.Errorf("tool request: %w", err)
	}
	if _, werr := conn.Write(append(body, '\n')); werr != nil {
		return nil, fmt.Errorf("tool request: %w", werr)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("tool response: %w", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("tool response: %w", err)
	}
	if !resp.OK {
		reason := resp.Reason
		if reason == "" {
			reason = resp.Error
		}
		return nil, fmt.Errorf("%s: %s", resp.Error, reason)
	}
	return resp.Result, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// starlarkCommand resolves the child executable for the embedded Starlark
// interpreter. Tests may inject Config.Self.
func (r *Runner) starlarkCommand() ([]string, error) {
	self := r.cfg.Self
	if self == "" {
		var err error
		self, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("%w: starlark executable: %w", ErrBackend, err)
		}
	}
	return []string{self, "__starlark"}, nil
}

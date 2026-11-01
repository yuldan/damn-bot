package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/netx"
)

// serveNetClient is the E-05-compliant egress client for the loopback server.
func serveNetClient() *netx.Client {
	return netx.New(netx.Config{}).Client("mcp-serve-e2e", netx.Inference)
}

// serveWaitFor polls until ok; local so Windows builds do not rely on a
// platform-specific harness helper.
func serveWaitFor(t *testing.T, limit time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if ok() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		case <-tick.C:
		}
	}
}

// serveHash mirrors the store's sha256-at-rest representation without
// importing the ctxdb consumer package from an e2e test (E-01).
func serveHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type serveHarness struct {
	e                      *Env
	base                   string
	read, approve, revoked string
	tokens                 []string
	stop                   func()
}

// newServeHarness starts the real binary's HTTP serve with one token per
// scope, so every test asserts on the wire, not in-process.
func newServeHarness(t *testing.T) *serveHarness {
	t.Helper()
	e := New(t)
	h := &serveHarness{e: e}
	h.read = serveTokenCreate(t, e, "read", "reader")
	h.approve = serveTokenCreate(t, e, "approve", "approver")
	h.revoked = serveTokenCreate(t, e, "read", "revoked")
	h.tokens = []string{h.read, h.approve, h.revoked}
	h.base, h.stop = startMCPServe(t, e, h.tokens...)
	t.Cleanup(h.stop)
	return h
}

// rpc posts one JSON-RPC frame and returns status, headers, and body. The
// body is always closed here so no caller can leak a connection.
func (h *serveHarness) rpc(t *testing.T, token, method, params string) (int, http.Header, string) {
	t.Helper()
	payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, params)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.base, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := serveNetClient().Do(req)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close body: %v", cerr)
		}
	}()
	var buf bytes.Buffer
	if _, cerr := buf.ReadFrom(resp.Body); cerr != nil {
		t.Fatalf("read body: %v", cerr)
	}
	return resp.StatusCode, resp.Header, buf.String()
}

func TestMCPServeHTTPAuthAndScope(t *testing.T) {
	h := newServeHarness(t)
	code, header, body := h.rpc(t, "", "tools/list", `{}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("no bearer = %d (%s), want 401", code, body)
	}
	if got := header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
	}
	if badCode, _, badBody := h.rpc(t, "tok-not-a-real-token", "tools/list", `{}`); badCode != http.StatusUnauthorized {
		t.Fatalf("wrong bearer = %d (%s), want 401", badCode, badBody)
	}
	code, _, body = h.rpc(t, h.read, "tools/list", `{}`)
	if code != http.StatusOK || !strings.Contains(body, `"recall"`) || !strings.Contains(body, `"sessions_read"`) {
		t.Fatalf("read tools/list = %d (%s)", code, body)
	}
	for _, hidden := range []string{"messages_send", "permissions_list_open", "permissions_respond"} {
		if strings.Contains(body, hidden) {
			t.Fatalf("read token enumerated %s:\n%s", hidden, body)
		}
	}
	call := `{"name":"messages_send","arguments":{"target":"owner","text":"hi"}}`
	code, _, body = h.rpc(t, h.read, "tools/call", call)
	if code != http.StatusForbidden || !strings.Contains(body, "-32003") {
		t.Fatalf("send outside scope = %d (%s), want 403/-32003", code, body)
	}
}

func TestMCPServeHTTPApproveLattice(t *testing.T) {
	h := newServeHarness(t)
	code, _, body := h.rpc(t, h.approve, "tools/list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("approve list = %d (%s)", code, body)
	}
	if !strings.Contains(body, "permissions_list_open") || !strings.Contains(body, "permissions_respond") {
		t.Fatalf("approve tools/list lacks approval tools:\n%s", body)
	}
	if strings.Contains(body, "recall") || strings.Contains(body, "messages_send") {
		t.Fatalf("approve token escaped its lattice:\n%s", body)
	}
}

func TestMCPServeHTTPRevokedBearer(t *testing.T) {
	h := newServeHarness(t)
	if code, _, body := h.rpc(t, h.revoked, "tools/list", `{}`); code != http.StatusOK {
		t.Fatalf("live token = %d (%s)", code, body)
	}
	prefix := serveHash(h.revoked)[:12]
	res := h.e.Exec("mcp", "serve", "--data-dir", h.e.DataDir, "token", "revoke", prefix)
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "revoked") {
		t.Fatalf("revoke = %q", res.Stdout)
	}
	if code, _, body := h.rpc(t, h.revoked, "tools/list", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("revoked token = %d (%s), want 401", code, body)
	}
}

// TestMCPServeHTTPApprovalInbox: permissions_respond answers a pending row of
// another origin, but never one the serve itself created (self-approval).
func TestMCPServeHTTPApprovalInbox(t *testing.T) {
	e := New(t)
	approveTok := serveTokenCreate(t, e, "approve", "approver")
	sha := strings.Repeat("a", 64)
	seedServeApproval(t, e.DataDir, "r-self", "mcp_serve", sha, 0)
	seedServeApproval(t, e.DataDir, "r-owner", "tui", sha, 0)
	h := &serveHarness{e: e, approve: approveTok}
	h.base, h.stop = startMCPServe(t, e, approveTok)
	t.Cleanup(h.stop)

	answer := func(id string) (int, string) {
		params := fmt.Sprintf(`{"name":"permissions_respond","arguments":{"request_id":%q,"decision":"allow_once","preview_sha":%q}}`, id, sha)
		code, _, body := h.rpc(t, approveTok, "tools/call", params)
		return code, body
	}
	code, body := answer("r-self")
	if code != http.StatusOK || !strings.Contains(body, `"isError":true`) ||
		!strings.Contains(body, "forbidden") || !strings.Contains(body, "mcp_serve") {
		t.Fatalf("self-approval = %d (%s), want an in-band refusal", code, body)
	}
	if n := countServeResponses(t, e.DataDir); n != 0 {
		t.Fatalf("self-approval stored %d response rows", n)
	}
	code, body = answer("r-owner")
	if code != http.StatusOK || strings.Contains(body, `"isError":true`) {
		t.Fatalf("owner-origin approval = %d (%s), want success", code, body)
	}
	if n := countServeResponses(t, e.DataDir); n != 1 {
		t.Fatalf("approval stored %d response rows, want 1", n)
	}
	list := e.Exec("mcp", "serve", "--data-dir", e.DataDir, "token", "list")
	list.RequireOK(t)
	if !strings.Contains(list.Stdout, "approver") {
		t.Fatalf("token list = %q", list.Stdout)
	}
}

func serveTokenCreate(t *testing.T, e *Env, scope, identity string) string {
	t.Helper()
	res := e.Exec("mcp", "serve", "--data-dir", e.DataDir, "token", "create",
		"--scope", scope, "--identity", identity)
	res.RequireOK(t)
	raw := strings.TrimSpace(res.Stdout)
	if raw == "" {
		t.Fatalf("token create printed no token: %q", res.Stdout)
	}
	list := e.Exec("mcp", "serve", "--data-dir", e.DataDir, "token", "list")
	list.RequireOK(t)
	if strings.Contains(list.Stdout, raw) || strings.Contains(list.Stdout, serveHash(raw)) {
		t.Fatalf("token list leaked the token or its hash:\n%s", list.Stdout)
	}
	return raw
}

// startMCPServe starts the real binary and waits until the loopback listener
// answers. stop kills the child and asserts the token never reached its logs.
func startMCPServe(t *testing.T, e *Env, secrets ...string) (string, func()) {
	t.Helper()
	lc := net.ListenConfig{}
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	addr := ln.Addr().String()
	if cerr := ln.Close(); cerr != nil {
		t.Fatalf("release port: %v", cerr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, binPaths.main, "mcp", "serve", "--listen", addr, "--data-dir", e.DataDir) // #nosec G204 -- this package's temp build
	cmd.Dir = e.Workdir
	cmd.Env = e.childEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start serve: %v", err)
	}
	base := "http://" + addr
	serveWaitFor(t, 20*time.Second, "mcp serve to answer", func() bool {
		req, rerr := http.NewRequestWithContext(t.Context(), http.MethodPost, base, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`)) // #nosec G107 -- loopback test listener
		if rerr != nil {
			return false
		}
		resp, rerr := serveNetClient().Do(req)
		if rerr != nil {
			return false
		}
		if cerr := resp.Body.Close(); cerr != nil {
			return false
		}
		return resp.StatusCode == http.StatusUnauthorized
	})
	stop := func() {
		cancel()
		if werr := cmd.Wait(); werr != nil && !errors.Is(werr, context.Canceled) {
			t.Logf("serve exit: %v", werr)
		}
		if !strings.Contains(errb.String(), "mcp serve: listening on http://") {
			t.Errorf("serve stderr lacks the listen line: %q", errb.String())
		}
		for _, leak := range secrets {
			if strings.Contains(out.String(), leak) || strings.Contains(errb.String(), leak) {
				t.Errorf("serve logs leaked a token")
			}
		}
	}
	return base, stop
}

func seedServeApproval(t *testing.T, dataDir, requestID, origin, previewSHA string, expires int64) {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dataDir, Kind: "mcp_serve"})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close seed db: %v", cerr)
		}
	}()
	payload, err := json.Marshal(map[string]any{
		"request_id": requestID, "tool": "messages_send", "class": "send", "origin": origin,
		"preview_sha": previewSHA, "channels": []string{"tui"}, "expires_at": expires,
	})
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	session := "s-" + requestID
	turn := "t-" + requestID
	err = db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.CreateSession(ctxdb.Session{ID: session, Cwd: "/work", Model: "p/m", ConfigSHA: "sha", ContextTier: "core"}); xerr != nil {
			return fmt.Errorf("create session: %w", xerr)
		}
		if _, xerr := tx.Exec(`INSERT INTO turn_journal(turn_id, session_id, state, approval_request, started_at, updated_at)
			VALUES (?1, ?2, 'awaiting_approval', ?3, 1, 2)`, turn, session, string(payload)); xerr != nil {
			return fmt.Errorf("insert journal: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed approval: %v", err)
	}
}

func countServeResponses(t *testing.T, dataDir string) int64 {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dataDir, Kind: "mcp_serve"})
	if err != nil {
		t.Fatalf("open count db: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close count db: %v", cerr)
		}
	}()
	var n int64
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if xerr := tx.Get(`SELECT count(*) FROM approval_responses`, nil, &n); xerr != nil {
			return fmt.Errorf("count: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

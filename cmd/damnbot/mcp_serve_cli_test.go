//go:build !minimal

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/mcpservestore"
	"github.com/damnbot/damnbot/internal/mcp"
	"github.com/damnbot/damnbot/internal/modules/host"
)

func TestMCPServeTokenCLI(t *testing.T) {
	mcpTestHome(t)
	code, out, errOut := runMCPArgs(t, "serve", "token", "create", "--scope", "read", "--identity", "owner")
	if code != 0 {
		t.Fatalf("token create = %d, err=%q", code, errOut)
	}
	raw := strings.TrimSpace(out)
	if raw == "" {
		t.Fatalf("create printed no token: %q", out)
	}
	code, out, errOut = runMCPArgs(t, "serve", "token", "list")
	if code != 0 {
		t.Fatalf("token list = %d, err=%q", code, errOut)
	}
	if strings.Contains(out, raw) || strings.Contains(out, mcpservestore.Hash(raw)) {
		t.Fatalf("token list leaked the token or its full hash:\n%s", out)
	}
	if !strings.Contains(out, "owner") || !strings.Contains(out, "read") || !strings.Contains(out, "active") {
		t.Fatalf("token list missing row:\n%s", out)
	}
	prefix := mcpservestore.Hash(raw)[:12]
	code, out, errOut = runMCPArgs(t, "serve", "token", "revoke", prefix)
	if code != 0 || !strings.Contains(out, "revoked") {
		t.Fatalf("token revoke = %d out=%q err=%q", code, out, errOut)
	}
	if code, _, _ := runMCPArgs(t, "serve", "token", "create", "--scope", "root", "--identity", "x"); code != 2 {
		t.Fatalf("bad scope = %d, want 2", code)
	}
}

// serveFixture builds a serve handler over a real ctxdb with one token.
func serveFixture(t *testing.T, scope string) (*ctxdb.Store, string, *serveHTTP) {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "mcp_serve"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	raw := "tok-" + scope + "-secret"
	store := mcpservestore.Store{DB: db}
	if cerr := store.Create(t.Context(), &mcpservestore.Token{
		IDHash: mcpservestore.Hash(raw), Profile: "", Identity: "client", Scope: scope, CreatedAt: time.Now().UnixMilli(),
	}); cerr != nil {
		t.Fatal(cerr)
	}
	srv, err := mcp.NewServe(&mcp.ServeConfig{
		Tokens: serveTokenStore{store: store}, Profile: "", Approvals: host.DenyApprovals{},
		Tools: mcpServeTools(db, t.TempDir()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, raw, &serveHTTP{srv: srv}
}

func postRPC(t *testing.T, h http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServeHTTPAuthAndScope(t *testing.T) {
	t.Parallel()
	_, raw, h := serveFixture(t, mcpservestore.ScopeRead)
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	if rec := postRPC(t, h, "", init); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token = %d, want 401", rec.Code)
	}
	if rec := postRPC(t, h, "wrong", init); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", rec.Code)
	}
	rec := postRPC(t, h, raw, init)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "protocolVersion") {
		t.Fatalf("initialize = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), raw) {
		t.Fatal("response echoed the token")
	}
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"messages_send","arguments":{"target":"telegram:1","text":"hi"}}}`
	if forbidden := postRPC(t, h, raw, call); forbidden.Code != http.StatusForbidden {
		t.Fatalf("read token send = %d %s, want 403", forbidden.Code, forbidden.Body.String())
	}
	list := `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`
	rec = postRPC(t, h, raw, list)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "messages_send") || !strings.Contains(rec.Body.String(), "recall") {
		t.Fatalf("read list = %d %s", rec.Code, rec.Body.String())
	}
}

func TestServeHTTPNonReadDeniedWithoutApprover(t *testing.T) {
	t.Parallel()
	_, raw, h := serveFixture(t, mcpservestore.ScopeSend)
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"messages_send","arguments":{"target":"telegram:1","text":"hi"}}}`
	if rec := postRPC(t, h, raw, call); rec.Code != http.StatusForbidden {
		t.Fatalf("non-read = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

func TestServeHTTPPermissionsRespondRefusesSelfApproval(t *testing.T) {
	t.Parallel()
	db, raw, h := serveFixture(t, mcpservestore.ScopeApprove)
	sha := strings.Repeat("a", 64)
	payload := `{"request_id":"r1","tool":"shell","class":"shell","origin":"mcp_serve","preview_sha":"` + sha + `","channels":["tui"]}`
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.CreateSession(ctxdb.Session{ID: "s1", Cwd: "/work", Model: "p/m", ConfigSHA: "sha"}); xerr != nil {
			return fmt.Errorf("create session: %w", xerr)
		}
		if _, xerr := tx.Exec(`INSERT INTO turn_journal(turn_id, session_id, state, approval_request, started_at, updated_at)
			VALUES ('t1','s1','awaiting_approval',?1,1,2)`, payload); xerr != nil {
			return fmt.Errorf("insert journal: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"permissions_respond","arguments":{"request_id":"r1","decision":"allow_once","preview_sha":"` + sha + `"}}}`
	rec := postRPC(t, h, raw, call)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("self approval = %d %s", rec.Code, rec.Body.String())
	}
	var n int64
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		var x int64
		if xerr := tx.Get(`SELECT count(*) FROM approval_responses`, nil, &x); xerr != nil {
			return fmt.Errorf("count responses: %w", xerr)
		}
		n = x
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("approval_responses rows = %d, want 0", n)
	}
}

func TestMCPToolsTable(t *testing.T) {
	t.Parallel()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "mcp_serve"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	tools := mcpServeTools(db, t.TempDir())
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	for _, want := range []string{"recall", "sessions_list", "sessions_read", "messages_send", "permissions_list_open", "permissions_respond"} {
		if !strings.Contains(strings.Join(names, ","), want) {
			t.Fatalf("tools = %v, missing %s", names, want)
		}
	}
}

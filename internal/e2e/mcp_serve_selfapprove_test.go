package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestMCPServeCannotSelfApprove (ADR-083): an mcp serve client may never
// approve an approval the serve itself created. A read/send client cannot
// answer approvals at all (scope lattice), and even the approve client is
// refused in band for an origin="mcp_serve" row; neither path stores a
// response. Everything runs through the real binary and the HTTP wire.
func TestMCPServeCannotSelfApprove(t *testing.T) {
	e := New(t)
	readTok := serveTokenCreate(t, e, "read", "reader")
	sendTok := serveTokenCreate(t, e, "send", "sender")
	approveTok := serveTokenCreate(t, e, "approve", "approver")
	sha := strings.Repeat("a", 64)
	seedServeApproval(t, e.DataDir, "r-self", "mcp_serve", sha, 0)
	h := &serveHarness{e: e}
	h.base, h.stop = startMCPServe(t, e, readTok, sendTok, approveTok)
	t.Cleanup(h.stop)

	answer := func(token, id string) (int, string) {
		params := fmt.Sprintf(`{"name":"permissions_respond","arguments":{"request_id":%q,"decision":"allow_once","preview_sha":%q}}`, id, sha)
		code, _, body := h.rpc(t, token, "tools/call", params)
		return code, body
	}

	// read/send tokens never reach the approval tool: the scope gate answers
	// before any approval logic runs.
	for _, token := range []string{readTok, sendTok} {
		code, body := answer(token, "r-self")
		if code != http.StatusForbidden || !strings.Contains(body, "-32003") {
			t.Fatalf("approval outside scope = %d (%s), want 403/-32003", code, body)
		}
	}

	// The approve client sees the tool, but a row it created itself is denied.
	code, body := answer(approveTok, "r-self")
	if code != http.StatusOK || !strings.Contains(body, `"isError":true`) ||
		!strings.Contains(body, "forbidden") || !strings.Contains(body, "mcp_serve") {
		t.Fatalf("self-approval = %d (%s), want an in-band refusal naming mcp_serve", code, body)
	}
	if n := countServeResponses(t, e.DataDir); n != 0 {
		t.Fatalf("self-approval stored %d response rows", n)
	}
}

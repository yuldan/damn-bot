//go:build unix

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// gwChatEnv is the identity the CLI speaks as. It is the env bootstrap serve
// seeded, so admission resolves the same row a platform update would.
func gwChatEnv(id int64) []string {
	return []string{fmt.Sprintf("DAMNBOT_GATEWAY_OWNER=telegram:%d", id)}
}

// TestGatewayChatCLIRoundTrip: GW-024 sends one line through the running
// serve's control spool, gets the turn answer exactly once, and leaves the DB
// single-writer rule intact (the CLI itself never opens the database).
func TestGatewayChatCLIRoundTrip(t *testing.T) {
	e := New(t, providertest.Text("pong from gateway"))
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	res := e.ExecStdinEnv("hello gateway\n", gwChatEnv(gatewayOwner),
		"gateway", "chat", "--platform", "telegram", "--data-dir", e.DataDir)
	if res.Code != 0 {
		t.Fatalf("chat: code=%d stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	if n := strings.Count(res.Stdout, "pong from gateway"); n != 1 {
		t.Fatalf("answer count = %d, want exactly 1 (stdout=%q)", n, res.Stdout)
	}
	if !strings.Contains(res.Stderr, "status: accepted") {
		t.Fatalf("missing accepted status: %q", res.Stderr)
	}
	if strings.Contains(res.Stdout, FakeTGToken) || strings.Contains(res.Stderr, FakeTGToken) {
		t.Fatal("token leaked to the chat output")
	}
	if n := len(e.Requests()); n != 1 {
		t.Fatalf("provider requests = %d, want 1: the turn runs in serve", n)
	}
	gatewayStop(t, p)
	db := gatewayDB(t, e)
	if n := gatewayCount(t, db, `SELECT count(*) FROM sessions WHERE origin = 'gateway'`); n != 1 {
		t.Fatalf("gateway sessions = %d, want 1", n)
	}
	if n := gatewayCount(t, db, `SELECT count(*) FROM gateway_inbound WHERE external_id LIKE 'cli:%'`); n != 1 {
		t.Fatalf("cli inbound rows = %d, want 1", n)
	}
}

// TestGatewayChatCLIAdmissionStillApplies: the CLI does not bypass the gate.
// A sender that serve never allowlisted is rejected and no turn runs.
func TestGatewayChatCLIAdmissionStillApplies(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	res := e.ExecStdinEnv("hello as a stranger\n", gwChatEnv(9999),
		"gateway", "chat", "--platform", "telegram", "--data-dir", e.DataDir)
	if res.Code == 0 {
		t.Fatalf("chat accepted a stranger: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "rejected: unknown_identity") {
		t.Fatalf("missing rejection: %q", res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "" {
		t.Fatalf("stranger produced output %q", res.Stdout)
	}
	if n := len(e.Requests()); n != 0 {
		t.Fatalf("provider requests = %d, want 0 before admission", n)
	}
	gatewayStop(t, p)
	db := gatewayDB(t, e)
	if n := gatewayCount(t, db, `SELECT count(*) FROM gateway_inbound WHERE external_id LIKE 'cli:%'`); n != 0 {
		t.Fatalf("denied cli inbound rows = %d, want 0", n)
	}
	if n := gatewayCount(t, db, `SELECT count(*) FROM sessions WHERE origin = 'gateway'`); n != 0 {
		t.Fatalf("gateway sessions = %d, want 0", n)
	}
}

// TestGatewayChatCLIRequiresServe: without the lease owner the CLI refuses at
// once with a clear error instead of hanging or opening a local one-shot turn.
func TestGatewayChatCLIRequiresServe(t *testing.T) {
	e := New(t)
	start := time.Now()
	res := e.ExecStdinEnv("hello\n", gwChatEnv(gatewayOwner),
		"gateway", "chat", "--platform", "telegram", "--data-dir", e.DataDir)
	if res.Code != 1 {
		t.Fatalf("chat without serve: code=%d stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "serve is not running") {
		t.Fatalf("missing refusal: %q", res.Stderr)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("refusal took %s, want < 5 s (not a hang)", elapsed)
	}
}

// TestGatewayChatCLIKeepsServeLease: a completed chat never takes the data-dir
// lease, so the original serve still owns it and a second serve is refused.
func TestGatewayChatCLIKeepsServeLease(t *testing.T) {
	e := New(t, providertest.Text("still here"))
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	res := e.ExecStdinEnv("ping\n", gwChatEnv(gatewayOwner),
		"gateway", "chat", "--platform", "telegram", "--data-dir", e.DataDir)
	if res.Code != 0 || !strings.Contains(res.Stdout, "still here") {
		t.Fatalf("chat: code=%d stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	second := exec.CommandContext(ctx, binPaths.main, "serve", "--config", e.cfg, "--data-dir", e.DataDir, "--bind", "127.0.0.1:0") // #nosec G204 -- binPaths.main is this package's temp test build, not user input
	second.Dir = e.Workdir
	second.Env = append(e.childEnv(), gatewayServeEnv(e, tg)...)
	out, err := second.CombinedOutput()
	if err == nil {
		t.Fatalf("second serve started while the first holds the lease:\n%s", out)
	}
	if !strings.Contains(string(out), "already owns this data dir") {
		t.Fatalf("second serve failed for a different reason:\n%s", out)
	}
	gatewayStop(t, p)
}

package e2e

import (
	"regexp"
	"strings"
	"testing"
)

var gatewayPairCode = regexp.MustCompile(`pairing code ([0-9]{6})`)

// TestGatewayCLIBinaryPairingQuota runs the real binary: five codes per hour
// per source, the sixth is refused.
func TestGatewayCLIBinaryPairingQuota(t *testing.T) {
	e := newEnv(t)
	env := []string{"DAMNBOT_GATEWAY_OWNER=telegram:1", "DAMNBOT_GATEWAY_ALLOW="}
	for i := range 5 {
		res := e.ExecEnv(env, "gateway", "pair", "telegram", "--data-dir", e.DataDir)
		if res.Code != 0 || !gatewayPairCode.MatchString(res.Stdout) {
			t.Fatalf("pair %d: code=%d stdout=%q stderr=%q", i, res.Code, res.Stdout, res.Stderr)
		}
	}
	sixth := e.ExecEnv(env, "gateway", "pair", "telegram", "--data-dir", e.DataDir)
	if sixth.Code == 0 || !strings.Contains(sixth.Stderr, "rate limit") {
		t.Fatalf("sixth pair: code=%d stderr=%q, want rate limit", sixth.Code, sixth.Stderr)
	}
}

// TestGatewayCLIBinaryAllowlist runs allow (idempotent), list, revoke and
// status against the real binary and data dir.
func TestGatewayCLIBinaryAllowlist(t *testing.T) {
	e := newEnv(t)
	env := []string{"DAMNBOT_GATEWAY_OWNER=telegram:1", "DAMNBOT_GATEWAY_ALLOW="}
	for range 2 {
		allow := e.ExecEnv(env, "gateway", "allow", "telegram", "42", "--data-dir", e.DataDir)
		if allow.Code != 0 || !strings.Contains(allow.Stdout, "allowed telegram:42 (role user)") {
			t.Fatalf("allow: code=%d stdout=%q stderr=%q", allow.Code, allow.Stdout, allow.Stderr)
		}
	}
	list := e.ExecEnv(env, "gateway", "list", "--data-dir", e.DataDir)
	if list.Code != 0 || !strings.Contains(list.Stdout, "telegram:42 role=user status=allowlisted") ||
		!strings.Contains(list.Stdout, "telegram:1 role=owner status=allowlisted") {
		t.Fatalf("list: code=%d stdout=%q stderr=%q", list.Code, list.Stdout, list.Stderr)
	}
	revoke := e.ExecEnv(env, "gateway", "revoke", "telegram", "42", "--data-dir", e.DataDir)
	if revoke.Code != 0 || !strings.Contains(revoke.Stdout, "blocked telegram:42") {
		t.Fatalf("revoke: code=%d stdout=%q stderr=%q", revoke.Code, revoke.Stdout, revoke.Stderr)
	}
	list = e.ExecEnv(env, "gateway", "list", "--data-dir", e.DataDir)
	if !strings.Contains(list.Stdout, "telegram:42 role=user status=blocked") {
		t.Fatalf("list after revoke: %q", list.Stdout)
	}
	status := e.ExecEnv(env, "gateway", "status", "--data-dir", e.DataDir)
	if status.Code != 0 || !strings.Contains(status.Stdout, "identities 2") ||
		!strings.Contains(status.Stdout, "owners 1") || !strings.Contains(status.Stdout, "blocked 1") {
		t.Fatalf("status: code=%d stdout=%q stderr=%q", status.Code, status.Stdout, status.Stderr)
	}
}

// TestGatewayCLIBinaryNonOwnerPair: a deployment with only role user
// identities cannot mint codes.
func TestGatewayCLIBinaryNonOwnerPair(t *testing.T) {
	e := newEnv(t)
	env := []string{"DAMNBOT_GATEWAY_OWNER=", "DAMNBOT_GATEWAY_ALLOW="}
	if allow := e.ExecEnv(env, "gateway", "allow", "telegram", "42", "--data-dir", e.DataDir); allow.Code != 0 {
		t.Fatalf("allow: %d %s", allow.Code, allow.Stderr)
	}
	pair := e.ExecEnv(env, "gateway", "pair", "telegram", "--data-dir", e.DataDir)
	if pair.Code == 0 || !strings.Contains(pair.Stderr, "owner") {
		t.Fatalf("non-owner pair: code=%d stderr=%q", pair.Code, pair.Stderr)
	}
}

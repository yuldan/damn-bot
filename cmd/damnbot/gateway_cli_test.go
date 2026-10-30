package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
)

// cliIDs adapts gwstore.Lookup to the consumer-side gateway.IdentityStore
// (the production wiring lands with cmd/damnbot in the wiring slice).
type cliIDs struct{ store gwstore.Store }

func (a cliIDs) Identity(ctx context.Context, platform, externalID string) (gateway.Identity, bool, error) {
	id, found, err := a.store.Lookup(ctx, platform, externalID)
	if err != nil {
		return gateway.Identity{}, false, fmt.Errorf("lookup: %w", err)
	}
	if !found {
		return gateway.Identity{}, false, nil
	}
	return gateway.Identity{
		Platform: platform, ExternalID: externalID, DisplayName: id.DisplayName,
		Role: gateway.Role(id.Role), Status: gateway.Status(id.Status),
		Strength: gateway.Strength(id.Strength), Workspace: id.Workspace,
	}, true, nil
}

type cliInbox struct{ calls int }

func (c *cliInbox) Record(context.Context, *gateway.Inbound) (bool, error) {
	c.calls++
	return true, nil
}

func runGatewayCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runGateway(args, &out, &errb)
	return code, out.String(), errb.String()
}

func gatewayTestDB(t *testing.T, dir string) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func closeTestDB(t *testing.T, db *ctxdb.Store) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func admit(store gwstore.Store, dataDir string, inbox *cliInbox) (gateway.Outcome, error) {
	g, err := gateway.New(&gateway.Options{DataDir: dataDir, Identities: cliIDs{store}, Inbox: inbox})
	if err != nil {
		return gateway.Outcome{}, fmt.Errorf("gateway: %w", err)
	}
	out, err := g.Admit(context.Background(), &gateway.Inbound{
		Platform: "telegram", ChatID: "c1", SenderID: "42", UpdateID: "u:1", Payload: `{"text":"hi"}`,
	}, func(context.Context, gateway.Ticket) error { return nil })
	if err != nil {
		return out, fmt.Errorf("admit: %w", err)
	}
	return out, nil
}

// TestGatewayCLIRevokeBlocksAdmission proves the CLI and gateway.Admit share
// one database: allow admits, revoke denies, with the runner untouched.
func TestGatewayCLIRevokeBlocksAdmission(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:1")
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", "")
	if code, _, errs := runGatewayCLI(t, "allow", "telegram", "42", "--data-dir", dir); code != 0 {
		t.Fatalf("allow: %d %s", code, errs)
	}
	db := gatewayTestDB(t, dir)
	inbox := &cliInbox{}
	out, err := admit(gwstore.Store{DB: db}, t.TempDir(), inbox)
	if err != nil || out.Kind != gateway.Accepted {
		t.Fatalf("admit after allow: %+v err=%v", out, err)
	}
	closeTestDB(t, db)
	if code, _, errs := runGatewayCLI(t, "revoke", "telegram", "42", "--data-dir", dir); code != 0 {
		t.Fatalf("revoke: %d %s", code, errs)
	}
	db = gatewayTestDB(t, dir)
	defer closeTestDB(t, db)
	out, err = admit(gwstore.Store{DB: db}, t.TempDir(), inbox)
	if err != nil || out.Kind != gateway.Rejected || out.Reason != gateway.ReasonNotAllowlisted {
		t.Fatalf("admit after revoke: %+v err=%v", out, err)
	}
	if inbox.calls != 1 {
		t.Fatalf("runner called %d times, want 1 (only the allowed update)", inbox.calls)
	}
}

func TestGatewayCLIPairBootstrapAndQuota(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "")
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", "")
	for i := range 5 {
		code, out, errs := runGatewayCLI(t, "pair", "telegram", "--data-dir", dir)
		if code != 0 || !strings.Contains(out, "pairing code ") {
			t.Fatalf("pair %d: %d out=%q err=%q", i, code, out, errs)
		}
	}
	if code, _, errs := runGatewayCLI(t, "pair", "telegram", "--data-dir", dir); code == 0 || !strings.Contains(errs, "rate limit") {
		t.Fatalf("sixth pair: code=%d err=%q, want rate limit", code, errs)
	}
}

func TestGatewayCLINonOwnerCannotPair(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "")
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", "")
	if code, _, errs := runGatewayCLI(t, "allow", "telegram", "42", "--data-dir", dir); code != 0 {
		t.Fatalf("allow user: %d %s", code, errs)
	}
	if code, _, errs := runGatewayCLI(t, "pair", "telegram", "--data-dir", dir); code == 0 || !strings.Contains(errs, "owner") {
		t.Fatalf("non-owner pair: code=%d err=%q", code, errs)
	}
}

func TestGatewayCLIListStatusAndEnvBootstrap(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:1")
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", "telegram:42")
	code, out, errs := runGatewayCLI(t, "list", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "telegram:1 role=owner status=allowlisted") ||
		!strings.Contains(out, "telegram:42 role=user status=allowlisted") {
		t.Fatalf("list: %d out=%q err=%q", code, out, errs)
	}
	code, out, errs = runGatewayCLI(t, "status", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "identities 2") || !strings.Contains(out, "owners 1") {
		t.Fatalf("status: %d out=%q err=%q", code, out, errs)
	}
}

func TestGatewayCLIRejectsBadID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "")
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", "")
	if code, _, _ := runGatewayCLI(t, "allow", "telegram", "-bad", "--data-dir", dir); code == 0 {
		t.Fatal("invalid identity accepted")
	}
}

// TestGatewayCLIEnvBootstrapJSON is EC-PLAT-021: DAMNBOT_GATEWAY_ALLOW may be
// a JSON list, its commas are not separators, and bootstrap normalises the
// set. A malformed entry fails the whole command before any write.
func TestGatewayCLIEnvBootstrapJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "")
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", `["telegram:42", "telegram:7", "telegram:42"]`)
	code, out, errs := runGatewayCLI(t, "list", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("list: %d %s", code, errs)
	}
	for _, want := range []string{"telegram:42 role=user", "telegram:7 role=user"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output %q misses %q", out, want)
		}
	}
	if strings.Count(out, "telegram:42 ") != 1 {
		t.Errorf("duplicate identity written: %q", out)
	}

	bad := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", "telegram:42, --bad")
	if code, _, errs := runGatewayCLI(t, "list", "--data-dir", bad); code != 1 || !strings.Contains(errs, "entry") {
		t.Fatalf("invalid allowlist: code=%d err=%q, want exit 1 naming the entry", code, errs)
	}
	db := gatewayTestDB(t, bad)
	defer closeTestDB(t, db)
	if n := countIdentities(t, db); n != 0 {
		t.Fatalf("bootstrap wrote %d identities despite an invalid entry, want 0", n)
	}
}

func countIdentities(t *testing.T, db *ctxdb.Store) int64 {
	t.Helper()
	var n int64
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT count(*) FROM gateway_identities", nil, &n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestGatewayPairingApproveClear: approve consumes the code once, replay is
// rejected, clear removes only pending codes and never allowlisted identities.
func pairCode(t *testing.T, out string) string {
	t.Helper()
	for f := range strings.FieldsSeq(out) {
		if len(f) == 6 {
			return f
		}
	}
	t.Fatalf("no pairing code in %q", out)
	return ""
}

func TestGatewayPairingApproveClear(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:1")
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", "")
	_, out, _ := runGatewayCLI(t, "pair", "telegram", "--data-dir", dir)
	code := pairCode(t, out)
	if c, o, e := runGatewayCLI(t, "pairing", "approve", code, "telegram:42", "--data-dir", dir); c != 0 || !strings.Contains(o, "approved telegram:42") {
		t.Fatalf("approve: %d %q %q", c, o, e)
	}
	if c, _, e := runGatewayCLI(t, "pairing", "approve", code, "telegram:43", "--data-dir", dir); c == 0 || !strings.Contains(e, "already used") {
		t.Fatalf("replay: %d %q", c, e)
	}
	_, out, _ = runGatewayCLI(t, "pair", "telegram", "--data-dir", dir)
	code = pairCode(t, out)
	if c, o, e := runGatewayCLI(t, "pairing", "clear-pending", "telegram", "--data-dir", dir); c != 0 || !strings.Contains(o, "cleared 1") {
		t.Fatalf("clear: %d %q %q", c, o, e)
	}
	if c, _, e := runGatewayCLI(t, "pairing", "approve", code, "telegram:44", "--data-dir", dir); c == 0 {
		t.Fatalf("cleared code still approved: %q", e)
	}
	if c, o, _ := runGatewayCLI(t, "list", "--data-dir", dir); c != 0 || !strings.Contains(o, "telegram:42") {
		t.Fatalf("identity lost by clear: %d %q", c, o)
	}
}

// TestGatewayEnrollAndMigrate: enroll is idempotent and allowlists; migrate
// status/plan are read-only and apply is idempotent.
func TestGatewayEnrollAndMigrate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:1")
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", "")
	for range 2 {
		if c, o, e := runGatewayCLI(t, "enroll", "telegram:42", "--data-dir", dir); c != 0 || !strings.Contains(o, "enrolled telegram:42") {
			t.Fatalf("enroll: %d %q %q", c, o, e)
		}
	}
	if c, o, e := runGatewayCLI(t, "list", "--data-dir", dir); c != 0 || !strings.Contains(o, "telegram:42") {
		t.Fatalf("list: %d %q %q", c, o, e)
	}
	if c, o, e := runGatewayCLI(t, "migrate", "status", "--data-dir", dir); c != 0 || !strings.Contains(o, "pending 0") {
		t.Fatalf("status: %d %q %q", c, o, e)
	}
	if c, o, e := runGatewayCLI(t, "migrate", "plan", "--data-dir", dir); c != 0 || strings.TrimSpace(o) != "" {
		t.Fatalf("plan: %d %q %q", c, o, e)
	}
	for range 2 {
		if c, o, e := runGatewayCLI(t, "migrate", "apply", "--data-dir", dir); c != 0 || !strings.Contains(o, "up to date") {
			t.Fatalf("apply: %d %q %q", c, o, e)
		}
	}
}

// TestGatewayPairingSurface: the parity aliases list pending codes, revoke
// blocks an identity, and migrate-legacy is an idempotent schema apply.
func TestGatewayPairingSurface(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:1")
	t.Setenv("DAMNBOT_GATEWAY_ALLOW", "")
	_, out, _ := runGatewayCLI(t, "pair", "telegram", "--data-dir", dir)
	if c, o, e := runGatewayCLI(t, "pairing", "list", "telegram", "--data-dir", dir); c != 0 || !strings.Contains(o, "pending 1") {
		t.Fatalf("list: %d %q %q", c, o, e)
	}
	if c, o, e := runGatewayCLI(t, "pairing", "revoke", "telegram", "42", "--data-dir", dir); c != 0 || !strings.Contains(o, "revoked telegram:42") {
		t.Fatalf("revoke: %d %q %q", c, o, e)
	}
	if c, o, e := runGatewayCLI(t, "migrate-legacy", "--data-dir", dir); c != 0 || !strings.Contains(o, "up to date") {
		t.Fatalf("migrate-legacy: %d %q %q", c, o, e)
	}
	if c, o, e := runGatewayCLI(t, "pairing", "approve", pairCode(t, out), "telegram:43", "--data-dir", dir); c != 0 || !strings.Contains(o, "approved telegram:43") {
		t.Fatalf("approve: %d %q %q", c, o, e)
	}
}

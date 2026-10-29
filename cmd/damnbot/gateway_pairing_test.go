package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
)

// gwPairing adapts the existing store to gateway.PairingStore (ADR-074): the
// gateway package never imports a store, and pairing stays
// gwstore.RedeemPairing rather than a second implementation. The production
// wiring lands with the serve worker, like cliIDs.
type gwPairing struct{ store gwstore.Store }

func (p gwPairing) RedeemPairing(ctx context.Context, platform, code, externalID string, now time.Time) error {
	_, err := p.store.RedeemPairing(ctx, platform, code, externalID, "platform_authenticated", now.UnixMilli())
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gwstore.ErrPairingUsed):
		return gateway.ErrPairingUsed
	case errors.Is(err, gwstore.ErrPairingExpired):
		return gateway.ErrPairingExpired
	case errors.Is(err, gwstore.ErrPairingQuota):
		return gateway.ErrPairingQuota
	case errors.Is(err, gwstore.ErrIdentityBlocked):
		return gateway.ErrPairingBlocked
	case errors.Is(err, gwstore.ErrInvalidPairing), errors.Is(err, gwstore.ErrInvalidIdentity):
		return gateway.ErrPairingInvalid
	default:
		return fmt.Errorf("gateway pairing: %w", err)
	}
}

func pairTicket(chat, user string) *gateway.Ticket {
	return &gateway.Ticket{Scope: gateway.Scope{Platform: "telegram", ChatID: chat, UserID: user}}
}

// pairEnv is one dispatcher over a real store, with a controllable clock.
type pairEnv struct {
	store  gwstore.Store
	cmds   *gateway.Commands
	now    time.Time
	issued int
}

func newPairEnv(t *testing.T) *pairEnv {
	t.Helper()
	db := gatewayTestDB(t, t.TempDir())
	t.Cleanup(func() { closeTestDB(t, db) })
	env := &pairEnv{
		store: gwstore.Store{DB: db},
		now:   time.Unix(1_700_000_000, 0),
	}
	if err := env.store.SetIdentity(context.Background(), "telegram", "900", "owner", "allowlisted", env.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	env.cmds = gateway.NewCommands(gateway.CommandOptions{
		Pairing: gwPairing{env.store},
		Now:     func() time.Time { return env.now },
	})
	return env
}

func (e *pairEnv) code(t *testing.T) string {
	t.Helper()
	e.issued++
	p, err := e.store.CreatePairing(context.Background(), "telegram", fmt.Sprintf("test-%d", e.issued), e.now.UnixMilli(), 10*time.Minute, 5)
	if err != nil {
		t.Fatalf("create pairing: %v", err)
	}
	return p.Code
}

func (e *pairEnv) dispatch(chat, user, text string) gateway.CommandResult {
	return e.cmds.Dispatch(context.Background(), pairTicket(chat, user), text)
}

func (e *pairEnv) lookup(t *testing.T, user string) (gwstore.Identity, bool) {
	t.Helper()
	id, found, err := e.store.Lookup(context.Background(), "telegram", user)
	if err != nil {
		t.Fatalf("lookup %s: %v", user, err)
	}
	return id, found
}

// TestPairCommandAllowlists is the end-to-end control of the dispatcher seam:
// the real gwstore redeems the code and the identity becomes allowlisted.
func TestPairCommandAllowlists(t *testing.T) {
	env := newPairEnv(t)
	res := env.dispatch("c1", "42", "/pair "+env.code(t))
	if !res.Handled || !strings.Contains(res.Reply, "telegram:42 is now allowlisted") {
		t.Fatalf("result = %+v", res)
	}
	id, found := env.lookup(t, "42")
	if !found || id.Status != "allowlisted" || id.Role != "user" {
		t.Fatalf("identity = %+v found=%v, want role user/allowlisted", id, found)
	}
}

// TestPairCommandUsedCode: the first redemption succeeds, the second is
// refused and changes nothing.
func TestPairCommandUsedCode(t *testing.T) {
	env := newPairEnv(t)
	code := env.code(t)
	if res := env.dispatch("c2", "43", "/pair "+code); !strings.Contains(res.Reply, "allowlisted") {
		t.Fatalf("first redemption = %+v, want success", res)
	}
	if res := env.dispatch("c2", "43", "/pair "+code); !strings.Contains(res.Reply, "already been used") {
		t.Fatalf("second redemption = %+v, want used", res)
	}
	if _, found := env.lookup(t, "43"); !found {
		t.Fatal("allowlisted identity disappeared")
	}
}

// TestPairCommandExpired: an expired code is refused and allowlists nobody.
func TestPairCommandExpired(t *testing.T) {
	env := newPairEnv(t)
	code := env.code(t)
	env.now = env.now.Add(11 * time.Minute)
	if res := env.dispatch("c3", "44", "/pair "+code); !strings.Contains(res.Reply, "has expired") {
		t.Fatalf("expired = %+v", res)
	}
	if _, found := env.lookup(t, "44"); found {
		t.Fatal("expired code allowlisted an identity")
	}
}

// TestPairCommandBlocked: the store refuses to promote a blocked identity.
func TestPairCommandBlocked(t *testing.T) {
	env := newPairEnv(t)
	if err := env.store.SetStatus(context.Background(), "telegram", "45", "blocked", env.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if res := env.dispatch("c4", "45", "/pair "+env.code(t)); !strings.Contains(res.Reply, "identity is blocked") {
		t.Fatalf("blocked = %+v", res)
	}
	id, found := env.lookup(t, "45")
	if !found || id.Status != "blocked" {
		t.Fatalf("blocked identity changed: %+v found=%v", id, found)
	}
}

func TestPairCommandMalformed(t *testing.T) {
	env := newPairEnv(t)
	if res := env.dispatch("c5", "46", "/pair abc"); !strings.Contains(res.Reply, "pairing code is invalid") {
		t.Fatalf("malformed = %+v", res)
	}
}

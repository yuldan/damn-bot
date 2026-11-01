package mcpservestore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestGAPF5_SEC21 (SPEC §22.1): the mcp-serve authority core. Tokens are
// bound to a profile and a closed scope; a remote responder is exactly-once,
// bound to the preview hash, never answers its own origin, and never answers a
// P26 class.
func TestGAPF5_SEC21(t *testing.T) {
	t.Parallel()
	db := open(t)
	store := Store{DB: db}
	ctx := context.Background()
	for _, scope := range []string{ScopeRead, ScopeSend, ScopeApprove} {
		raw := "tok_" + scope + "_F5"
		tok := Token{IDHash: Hash(raw), Profile: "default", Identity: "owner", Scope: scope, CreatedAt: 10}
		if err := store.Create(ctx, &tok); err != nil {
			t.Fatalf("Create %s: %v", scope, err)
		}
		got, err := store.Authenticate(ctx, raw, "default")
		if err != nil || got.Scope != scope {
			t.Fatalf("Authenticate %s = %+v, %v", scope, got, err)
		}
		if _, err := store.Authenticate(ctx, raw, "other"); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("%s crossed profile: %v", scope, err)
		}
	}

	sha := strings.Repeat("a", 64)
	seedApproval(t, db, "r1", payload("r1", "tui", "write", 0))
	first, err := store.Respond(ctx, &RespondRequest{
		RequestID: "r1", Decision: "allow_once", PreviewSHA: sha,
		Responder: "owner", ResponderTokenHash: sha, Now: 100,
	})
	if err != nil || first.TurnID != "t1" || first.Origin != "tui" {
		t.Fatalf("first respond = %+v, %v", first, err)
	}
	if _, err := store.Respond(ctx, &RespondRequest{
		RequestID: "r1", Decision: "deny", PreviewSHA: sha,
		Responder: "owner", ResponderTokenHash: sha, Now: 101,
	}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second respond = %v, want ErrNotPending (exactly once)", err)
	}

	cases := []struct {
		name, origin, class string
	}{
		{"self", "mcp_serve", "write"},
		{"p26_irreversible", "tui", "irreversible"},
		{"p26_monetary", "web", "monetary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := open(t)
			subStore := Store{DB: sub}
			id := "r-" + tc.name
			seedApproval(t, sub, id, payload(id, tc.origin, tc.class, 0))
			_, rerr := subStore.Respond(context.Background(), &RespondRequest{
				RequestID: id, Decision: "allow_once", PreviewSHA: sha,
				Responder: "owner", ResponderTokenHash: sha, Now: 100,
			})
			if !errors.Is(rerr, ErrForbidden) {
				t.Fatalf("respond = %v, want ErrForbidden", rerr)
			}
		})
	}
}

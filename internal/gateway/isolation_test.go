package gateway_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// TestGatewayUserRootIsolation is ADR-084: two role user identities never
// share a workspace root, and an owner keeps the serve cwd instead.
func TestGatewayUserRootIsolation(t *testing.T) { //nolint:gocyclo // one acceptance matrix for root isolation
	dataDir := t.TempDir()
	ids := map[string]gateway.Identity{
		"telegram:1": allow(&gateway.Identity{Platform: "telegram", ExternalID: "1", Role: gateway.RoleUser}),
		"telegram:2": allow(&gateway.Identity{Platform: "telegram", ExternalID: "2", Role: gateway.RoleUser}),
		"telegram:9": owner("9"),
	}
	g, _ := newGate(t, ids, nil, dataDir)
	run := &runner{}

	first, err := g.Admit(t.Context(), inbound("c", "1", "u:1"), run.run)
	if err != nil || first.Kind != gateway.Accepted {
		t.Fatalf("user 1: %+v err=%v", first, err)
	}
	second, err := g.Admit(t.Context(), inbound("c", "2", "u:2"), run.run)
	if err != nil || second.Kind != gateway.Accepted {
		t.Fatalf("user 2: %+v err=%v", second, err)
	}
	root1 := first.Ticket.Scope.Workspace
	root2 := second.Ticket.Scope.Workspace
	for _, c := range []struct {
		name, got, want string
	}{
		{"user 1 derived root", root1, filepath.Join(dataDir, "gw-workspaces", hash("telegram", "1"))},
		{"user 2 derived root", root2, filepath.Join(dataDir, "gw-workspaces", hash("telegram", "2"))},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if root1 == root2 {
		t.Fatalf("two identities share workspace %q", root1)
	}
	if !strings.HasPrefix(root1, dataDir+string(filepath.Separator)) ||
		!strings.HasPrefix(root2, dataDir+string(filepath.Separator)) {
		t.Fatalf("derived root escaped data dir: %q %q", root1, root2)
	}

	// An explicit config override wins over the derived default and stays per
	// identity: user 2 is unaffected.
	g, _ = newGate(t, ids, map[string]string{"telegram:1": "/srv/only-1"}, dataDir)
	override, err := g.Admit(t.Context(), inbound("c", "1", "u:1"), run.run)
	if err != nil || override.Ticket.Scope.Workspace != filepath.FromSlash("/srv/only-1") {
		t.Fatalf("override: %+v err=%v", override, err)
	}
	other, err := g.Admit(t.Context(), inbound("c", "2", "u:2"), run.run)
	if err != nil || other.Ticket.Scope.Workspace != root2 {
		t.Fatalf("override leaked to user 2: %+v err=%v", other, err)
	}

	own, err := g.Admit(t.Context(), inbound("c", "9", "u:3"), run.run)
	if err != nil || own.Ticket.Scope.Workspace != "" {
		t.Fatalf("owner root = %q, want empty (serve cwd)", own.Ticket.Scope.Workspace)
	}
}

// fakeBlobs is the blob ownership source: one owner principal per sha.
type fakeBlobs struct {
	owners map[string]string
	calls  int
	err    error
}

func (f *fakeBlobs) BlobOwned(_ context.Context, sha, platform, identity string) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.owners[sha] == platform+":"+identity, nil
}

// TestGatewayUserBlobScope is ADR-084's read exception: an identity reads its
// own blob and never another identity's.
func TestGatewayUserBlobScope(t *testing.T) { //nolint:gocyclo // one acceptance matrix for blob isolation
	shaA := strings.Repeat("a", 64)
	shaB := strings.Repeat("b", 64)
	blobs := &fakeBlobs{owners: map[string]string{shaA: "telegram:1", shaB: "telegram:2"}}
	guard, err := gateway.NewBlobGuard(blobs)
	if err != nil {
		t.Fatal(err)
	}
	a := gateway.Scope{Platform: "telegram", UserID: "1", ChatID: "c"}
	b := gateway.Scope{Platform: "telegram", UserID: "2", ChatID: "c"}

	if ok, err := guard.Allow(t.Context(), &a, shaA); err != nil || !ok {
		t.Fatalf("A reading own blob: ok=%v err=%v", ok, err)
	}
	if ok, err := guard.Allow(t.Context(), &a, shaB); err != nil || ok {
		t.Fatalf("A read B's blob: ok=%v err=%v", ok, err)
	}
	if ok, err := guard.Allow(t.Context(), &b, shaA); err != nil || ok {
		t.Fatalf("B read A's blob: ok=%v err=%v", ok, err)
	}
	calls := blobs.calls
	if ok, err := guard.Allow(t.Context(), &a, "not-a-sha"); err != nil || ok {
		t.Fatalf("malformed sha: ok=%v err=%v", ok, err)
	}
	if blobs.calls != calls {
		t.Fatalf("malformed sha reached the store")
	}
	if ok, err := guard.Allow(t.Context(), nil, shaA); err != nil || ok {
		t.Fatalf("nil scope: ok=%v err=%v", ok, err)
	}

	blobs.err = errors.New("db down")
	if ok, err := guard.Allow(t.Context(), &a, shaA); err == nil || ok {
		t.Fatalf("store failure must deny: ok=%v err=%v", ok, err)
	}
	if _, err := gateway.NewBlobGuard(nil); !errors.Is(err, gateway.ErrNoBlobStore) {
		t.Fatalf("nil store: %v", err)
	}
}

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/mcp"
)

// TestMCPPinStoreRoundTrip pins the CLI adapter to the module seam: what the
// client writes is what the store reads, including state and digest.
func TestMCPPinStoreRoundTrip(t *testing.T) {
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	pins, perr := pinStoreFor(db)
	if perr != nil {
		t.Fatal(perr)
	}
	if err := pins.PutPin(context.Background(), mcp.Pin{
		Server: "demo", Tool: "search", SchemaSHA256: strings.Repeat("a", 64),
		FirstSeen: 10, ApprovedAt: 10, ApprovedBy: "first-seen",
		State: mcp.PinChanged, Diff: `{"change":"schema"}`,
	}); err != nil {
		t.Fatalf("put pin: %v", err)
	}
	got, gerr := pins.Pins(context.Background(), "demo")
	if gerr != nil || len(got) != 1 {
		t.Fatalf("pins = %v, %v; want one", got, gerr)
	}
	if got[0].Tool != "search" || got[0].State != mcp.PinChanged || got[0].SchemaSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("pin = %+v, want changed/search", got[0])
	}
	ok, aerr := pins.ApprovePin(context.Background(), "demo", "search", strings.Repeat("a", 64), "cli", 20)
	if aerr != nil || !ok {
		t.Fatalf("approve = %v, %v; want true", ok, aerr)
	}
}

func TestMCPRefreshAndReviewUsage(t *testing.T) {
	mcpTestHome(t)
	for _, args := range [][]string{{"refresh", "a", "b"}, {"review", "a", "b"}} {
		if code, _, _ := runMCPArgs(t, args...); code != 2 {
			t.Fatalf("%q = %d, want 2", args, code)
		}
	}
	code, _, errOut := runMCPArgs(t, "refresh")
	if code != 1 || !strings.Contains(errOut, "no server selected") {
		t.Fatalf("refresh empty = %d %q, want 1/no server selected", code, errOut)
	}
	code, _, errOut = runMCPArgs(t, "review")
	if code != 1 || !strings.Contains(errOut, "no server selected") {
		t.Fatalf("review empty = %d %q, want 1/no server selected", code, errOut)
	}
}

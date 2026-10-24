package main

import (
	"strings"
	"testing"
)

// TestSessionsContextPin (MEM-025, ADR-186): the CLI pins a transcript
// position, lists it, releases it with --off and rejects a bad --at.
func TestSessionsContextPin(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	if code, out, e := runAdmin(t, "context-pin", id, "--data-dir", dir); code != 0 || !strings.Contains(out, "no pinned context") {
		t.Fatalf("list = %d %q (%s)", code, out, e)
	}
	if code, out, e := runAdmin(t, "context-pin", id, "--at", "2", "--data-dir", dir); code != 0 || !strings.Contains(out, "pinned 2") {
		t.Fatalf("pin = %d %q (%s)", code, out, e)
	}
	if code, out, e := runAdmin(t, "context-pin", id, "--data-dir", dir); code != 0 || !strings.Contains(out, "pin 2") {
		t.Fatalf("list after pin = %d %q (%s)", code, out, e)
	}
	// "newest" names the newest session, so a script can pin without an id.
	if code, out, e := runAdmin(t, "context-pin", "newest", "--at", "3", "--data-dir", dir); code != 0 || !strings.Contains(out, "pinned 3") {
		t.Fatalf("pin newest = %d %q (%s)", code, out, e)
	}
	if code, _, e := runAdmin(t, "context-pin", id, "--at", "0", "--data-dir", dir); code != 2 || !strings.Contains(e, "positive seq") {
		t.Errorf("seq 0 = %d %q, want exit 2", code, e)
	}
	if code, _, e := runAdmin(t, "context-pin", id, "--data-dir", dir, "extra"); code != 2 {
		t.Errorf("two ids = %d %q, want exit 2", code, e)
	}
	if code, out, e := runAdmin(t, "context-pin", id, "--at", "2", "--off", "--data-dir", dir); code != 0 || !strings.Contains(out, "unpinned 2") {
		t.Fatalf("unpin = %d %q (%s)", code, out, e)
	}
	if code, out, _ := runAdmin(t, "context-pin", id, "--data-dir", dir); code != 0 || strings.Contains(out, "pin 2") || !strings.Contains(out, "pin 3") {
		t.Fatalf("after unpin = %q, want only pin 3", out)
	}
}

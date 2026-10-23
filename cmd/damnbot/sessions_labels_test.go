package main

import (
	"strings"
	"testing"
)

// TestSessionsStatsAndFlags: pin/unpin/archive drive the stats counters.
func TestSessionsStatsAndFlags(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	if code, _, e := runAdmin(t, "pin", id, "--data-dir", dir); code != 0 {
		t.Fatalf("pin = %d: %s", code, e)
	}
	if code, out, _ := runAdmin(t, "pinned", "--data-dir", dir); code != 0 || !strings.Contains(out, id) {
		t.Fatalf("pinned = %d %q", code, out)
	}
	if code, out, _ := runAdmin(t, "stats", "--data-dir", dir); code != 0 || !strings.Contains(out, "pinned: 1") || !strings.Contains(out, "events: 2") {
		t.Fatalf("stats = %d:\n%s", code, out)
	}
	if code, _, e := runAdmin(t, "archive", id, "--data-dir", dir); code != 0 {
		t.Fatalf("archive = %d: %s", code, e)
	}
	if code, out, _ := runAdmin(t, "stats", "--data-dir", dir); code != 0 || !strings.Contains(out, "archived: 1") {
		t.Fatalf("stats after archive = %d:\n%s", code, out)
	}
	if code, _, e := runAdmin(t, "unpin", id, "--data-dir", dir); code != 0 {
		t.Fatalf("unpin = %d: %s", code, e)
	}
	if code, _, _ := runAdmin(t, "pin", "--data-dir", dir); code != 2 {
		t.Errorf("pin without id exit = %d, want 2", code)
	}
}

// TestSessionsLabelArgErrors: the flag/listing commands reject a missing or
// extra id with exit 2 before they touch the store (no silent write).
func TestSessionsLabelArgErrors(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	for _, args := range [][]string{
		{"pin", "--data-dir", dir},
		{"unpin", "--data-dir", dir},
		{"archive", "--data-dir", dir},
		{"unarchive", "--data-dir", dir},
		{"stats", id, "--data-dir", dir},
		{"pinned", id, "--data-dir", dir},
		{"pin", id, "extra", "--data-dir", dir},
	} {
		if code, _, _ := runAdmin(t, args...); code != 2 {
			t.Errorf("sessions %v exit = %d, want 2", args, code)
		}
	}
	if code, out, _ := runAdmin(t, "stats", "--data-dir", dir); code != 0 || !strings.Contains(out, "archived: 0") || !strings.Contains(out, "pinned: 0") {
		t.Errorf("rejected arguments mutated the store: %d\n%s", code, out)
	}
}

// TestSessionsFlagUnknownID: labelling a session that does not exist fails
// with the not_found mapping (exit 1) and writes nothing.
func TestSessionsFlagUnknownID(t *testing.T) {
	t.Parallel()
	dir, _ := seedSession(t)
	if code, _, e := runAdmin(t, "archive", "S-absent", "--data-dir", dir); code != 1 || !strings.Contains(e, "not found") {
		t.Fatalf("archive unknown id = %d %q, want exit 1 not_found", code, e)
	}
	if code, out, _ := runAdmin(t, "stats", "--data-dir", dir); code != 0 || !strings.Contains(out, "archived: 0") {
		t.Errorf("stats after unknown id = %d:\n%s", code, out)
	}
}

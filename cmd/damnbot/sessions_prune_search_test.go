package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestSessionsPruneTargetShape: the target forms of `sessions prune` are
// mutually exclusive and an id never silently implies --archive.
func TestSessionsPruneTargetShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    adminArgs
		wantErr string
	}{
		{name: "id", args: adminArgs{pos: []string{"S1"}}},
		{name: "id_yes_with_forks", args: adminArgs{pos: []string{"S1"}, yes: true, withForks: true}},
		{name: "older_than", args: adminArgs{olderThan: "90d", yes: true}},
		{name: "no_target", args: adminArgs{}, wantErr: "required"},
		{name: "two_ids", args: adminArgs{pos: []string{"S1", "S2"}}, wantErr: "one session id"},
		{name: "id_and_age", args: adminArgs{pos: []string{"S1"}, olderThan: "90d"}, wantErr: "not both"},
		{name: "id_archive", args: adminArgs{pos: []string{"S1"}, archive: true}, wantErr: "--archive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := pruneTarget(&tc.args)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("pruneTarget(%+v) = %v", tc.args, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("pruneTarget(%+v) = %v, want error containing %q", tc.args, err, tc.wantErr)
			}
		})
	}
}

// TestSessionsPruneRefusesDestructiveFormsInProcess: the destructive paths
// fail before opening a store (missing --yes, exec guard) and never delete.
func TestSessionsPruneRefusesDestructiveFormsInProcess(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "prune"}, &out, &errb); code != 2 {
		t.Fatalf("prune without target = %d, want 2", code)
	}
	if code := run([]string{"sessions", "prune", "S1", "--archive", "--data-dir", t.TempDir()}, &out, &errb); code != 2 {
		t.Fatalf("prune <id> --archive = %d, want 2", code)
	}
	if code := run([]string{"sessions", "prune", "S1", "--older-than", "90d", "--data-dir", t.TempDir()}, &out, &errb); code != 2 {
		t.Fatalf("prune <id> --older-than = %d, want 2", code)
	}
	if code := run([]string{"sessions", "prune", "S1", "--data-dir", t.TempDir()}, &out, &errb); code != 2 {
		t.Fatalf("prune <id> without --yes = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "--yes") && !strings.Contains(errb.String(), "--archive") &&
		!strings.Contains(errb.String(), "not both") && !strings.Contains(errb.String(), "required") {
		t.Errorf("stderr = %q", errb.String())
	}
}

// TestSessionsSearchArgs: the query is required, words are joined, and a
// negative limit is a usage error.
func TestSessionsSearchArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		args      []string
		wantQuery string
		wantErr   bool
	}{
		{name: "words", args: []string{"migration", "plan"}, wantQuery: "migration plan"},
		{name: "flags_before", args: []string{"--session", "S1", "hello"}, wantQuery: "hello"},
		{name: "flags_after", args: []string{"hello", "--limit", "3"}, wantQuery: "hello"},
		{name: "empty", args: nil, wantErr: true},
		{name: "negative_limit", args: []string{"hello", "--limit", "-1"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var errb bytes.Buffer
			a, err := parseSearchArgs(tc.args, &errb)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSearchArgs(%q) = nil error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSearchArgs(%q): %v", tc.args, err)
			}
			if a.query != tc.wantQuery {
				t.Errorf("query = %q, want %q", a.query, tc.wantQuery)
			}
		})
	}
}

// TestSessionsSearchEmptyQueryExits2: `sessions search` with no query is a
// usage error, never a silent empty result.
func TestSessionsSearchEmptyQueryExits2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "search"}, &out, &errb); code != 2 {
		t.Fatalf("sessions search = %d, want 2", code)
	}
}

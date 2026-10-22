package ctxdb

import "testing"

// TestSQLiteEngineVersion (GAP-REL-04): the linked engine satisfies the
// version floor and the FTS5/contentless_delete/trigram/json probes.
func TestSQLiteEngineVersion(t *testing.T) {
	t.Parallel()
	e, err := ProbeEngine(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sqlite %s fts5=%v contentless_delete=%v trigram=%v json=%v",
		e.Version, e.FTS5, e.ContentlessDelete, e.Trigram, e.JSON)
	if !e.Supported() {
		t.Fatalf("sqlite %s does not satisfy REL-04 (min %s)", e.Version, MinSQLiteVersion)
	}
	for _, c := range []struct {
		major, minor, patch int
		want                bool
	}{
		{3, 51, 3, true}, {3, 51, 2, false}, {3, 53, 0, true}, {4, 0, 0, true}, {3, 43, 0, false},
	} {
		if got := atLeast(c.major, c.minor, c.patch, MinSQLiteVersion); got != c.want {
			t.Errorf("atLeast(%d.%d.%d) = %v, want %v", c.major, c.minor, c.patch, got, c.want)
		}
	}
}

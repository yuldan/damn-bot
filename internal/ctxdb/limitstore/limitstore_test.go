package limitstore

import (
	"errors"
	"testing"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func open(t *testing.T, dir string) *ctxdb.Store {
	t.Helper()
	s, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type obs struct {
	window int64
	source string
	at     int64
}

func load(t *testing.T, s Store, model string) obs {
	t.Helper()
	w, src, at, err := s.Load(t.Context(), "p", model)
	if err != nil {
		t.Fatal(err)
	}
	return obs{w, src, at}
}

// TestModelLimitsRoundTrip: observations survive closing and reopening the
// database; a missing row is window 0, not an error.
func TestModelLimitsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db := open(t, dir)
	s := Store{DB: db}
	if got := load(t, s, "m"); got != (obs{}) {
		t.Errorf("empty table: %+v", got)
	}
	if err := s.Save(t.Context(), "p", "m", 16_384, "learned", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), "p", "other", 200_000, "discovery", 2000); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	s = Store{DB: db}
	if got := load(t, s, "m"); got != (obs{16_384, "learned", 1000}) {
		t.Errorf("after reopen: %+v", got)
	}
	if got := load(t, s, "other"); got != (obs{200_000, "discovery", 2000}) {
		t.Errorf("after reopen: %+v", got)
	}
}

// TestModelLimitsDiscoveryOverLearned (GAP-COST-02): discovery replaces a
// learned or older discovered row; learned replaces only learned.
func TestModelLimitsDiscoveryOverLearned(t *testing.T) {
	t.Parallel()
	db := open(t, t.TempDir())
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	s := Store{DB: db}
	steps := []struct {
		window int64
		source string
		want   obs
	}{
		{16_384, "learned", obs{16_384, "learned", 1}},
		{12_000, "learned", obs{12_000, "learned", 2}},       // learned refines learned
		{131_072, "discovery", obs{131_072, "discovery", 3}}, // discovery outranks learned
		{8_192, "learned", obs{131_072, "discovery", 3}},     // learned never overrides discovery
		{65_536, "discovery", obs{65_536, "discovery", 5}},   // newer discovery wins
	}
	for i, st := range steps {
		if err := s.Save(t.Context(), "p", "m", st.window, st.source, int64(i+1)); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got := load(t, s, "m"); got != st.want {
			t.Errorf("step %d (%s %d): %+v, want %+v", i, st.source, st.window, got, st.want)
		}
	}
	for _, src := range []string{"config", "registry", "fallback", ""} {
		if err := s.Save(t.Context(), "p", "m", 50_000, src, 9); !errors.Is(err, ErrSource) {
			t.Errorf("source %q: %v, want ErrSource", src, err)
		}
	}
	if err := s.Save(t.Context(), "p", "zero", 0, "learned", 9); err == nil {
		t.Error("window 0 accepted (CHECK context_window > 0)")
	}
}

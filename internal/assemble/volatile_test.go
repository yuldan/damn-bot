package assemble

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestVolatileTierCaps(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	turn := a.Freeze(fixtureVolatile())
	v := turn.Volatile()
	c := asciiCounter()
	parts := []struct {
		name string
		text string
		cap  int
	}{
		{"time", v.Time, CapVolTime},
		{"budget", v.Budget, CapVolBudget},
		{"todo", v.Todo, CapVolTodo},
		{"names", v.Names, CapVolNames},
		{"notices", v.Notices, CapVolNotices},
		{"volatile", turn.String(), CapVolatile},
	}
	for _, p := range parts {
		if n := c.Count(p.text); n > p.cap {
			t.Errorf("%s = %d tokens, cap %d (%q)", p.name, n, p.cap, p.text)
		}
	}
	for line := range strings.SplitSeq(v.Todo, "\n") {
		if strings.HasPrefix(line, "- ") && utf8.RuneCountInString(line) > TodoTextChars+2 {
			t.Errorf("todo line exceeds %d chars: %q", TodoTextChars, line)
		}
	}
	if strings.Contains(v.Todo, "completed item") {
		t.Error("done todo rendered")
	}
}

func TestVolatileFrozen(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	in := fixtureVolatile()
	turn := a.Freeze(in)
	first := turn.String()
	in.Todos = []Todo{{Text: "changed after freeze", Status: "pending"}}
	in.Notices = []string{"changed after freeze"}
	if second := turn.String(); first != second {
		t.Errorf("frozen turn changed:\n%s\n---\n%s", first, second)
	}
}

func TestVolatileEmpty(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	turn := a.Freeze(&VolatileInput{})
	if turn.String() != "" {
		t.Errorf("empty volatile tier = %q", turn.String())
	}
}

func TestVolatileNoticeMerged(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	turn := a.Freeze(&VolatileInput{Notices: []string{"first", "second", "third"}})
	if got := turn.Volatile().Notices; !strings.Contains(got, "first") || !strings.Contains(got, "+2 more") {
		t.Errorf("notice slot = %q", got)
	}
}

func TestVolatileTimeUTC(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	turn := a.Freeze(&VolatileInput{Now: time.Date(2026, 10, 19, 23, 5, 0, 0, time.FixedZone("X", 3600))})
	if got := turn.Volatile().Time; !strings.Contains(got, "2026-10-19 22:05") {
		t.Errorf("time = %q", got)
	}
}

// TestVolatileTimeIdentityZone (PROV-03): the identity's IANA zone renders a
// short local stamp with its abbreviation and weekday, inside CapVolTime.
func TestVolatileTimeIdentityZone(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	a := newAssembler(t, identity)
	now := time.Date(2026, 10, 17, 7, 5, 0, 0, time.UTC)
	turn := a.Freeze(&VolatileInput{Now: now, Loc: loc})
	got := turn.Volatile().Time
	if !strings.Contains(got, "2026-10-17 14:05 WIB Sat") {
		t.Fatalf("time = %q", got)
	}
	if n := asciiCounter().Count(got); n > CapVolTime {
		t.Fatalf("time = %d tokens > %d (%q)", n, CapVolTime, got)
	}
}

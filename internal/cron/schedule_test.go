package cron

import (
	"testing"
	"time"
)

func TestParseScheduleErrors(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "a * * * *", "*/0 * * * *", "5-1 * * * *"} {
		if _, err := ParseSchedule(expr, time.UTC); err == nil {
			t.Errorf("ParseSchedule(%q) = nil error, want error", expr)
		}
	}
}

func TestScheduleNext(t *testing.T) {
	loc := time.UTC
	base := time.Date(2026, 10, 29, 10, 7, 0, 0, loc)
	for _, tc := range []struct {
		expr string
		want time.Time
	}{
		{"*/15 * * * *", time.Date(2026, 10, 29, 10, 15, 0, 0, loc)},
		{"0 11 * * *", time.Date(2026, 10, 29, 11, 0, 0, 0, loc)},
		{"5,35 * * * *", time.Date(2026, 10, 29, 10, 35, 0, 0, loc)},
		{"0 0 * * 0", time.Date(2026, 11, 1, 0, 0, 0, 0, loc)}, // next Sunday
		{"0 0 1 * *", time.Date(2026, 11, 1, 0, 0, 0, 0, loc)},
	} {
		s, err := ParseSchedule(tc.expr, loc)
		if err != nil {
			t.Fatalf("ParseSchedule(%q): %v", tc.expr, err)
		}
		if got := s.Next(base); !got.Equal(tc.want) {
			t.Errorf("%q Next = %s, want %s", tc.expr, got, tc.want)
		}
	}
}

// TestScheduleDayOrRule pins cron's OR semantics between day-of-month and
// day-of-week when both are restricted.
func TestScheduleDayOrRule(t *testing.T) {
	s, err := ParseSchedule("0 0 1 * 1", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-29 is a Thursday; Nov 1 matches day-of-month 1 before Monday the 2nd.
	if got, want := s.Next(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("OR rule Next = %s, want %s", got, want)
	}
}

func TestScheduleImpossibleIsZero(t *testing.T) {
	s, err := ParseSchedule("0 0 31 2 *", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Errorf("Feb 31 Next = %s, want zero", got)
	}
}

// TestScheduleSlots checks the (from, to] window used by catch-up.
func TestScheduleSlots(t *testing.T) {
	s, err := ParseSchedule("*/15 * * * *", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Slots(at(10, 1), at(11, 2))
	if len(got) != 4 {
		t.Fatalf("slots = %d (%v), want 4", len(got), got)
	}
	if !got[0].Equal(at(10, 15)) || !got[3].Equal(at(11, 0)) {
		t.Errorf("slots = %v, want 10:15..11:00", got)
	}
}

// TestScheduleDSTNewYork (PROV-03): "0 2 * * *" in America/New_York fires
// exactly once on each local day across both 2026 DST transitions.
func TestScheduleDSTNewYork(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSchedule("0 2 * * *", loc)
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range []time.Time{
		time.Date(2026, 3, 7, 0, 0, 0, 0, loc),
		time.Date(2026, 10, 31, 0, 0, 0, 0, loc),
	} {
		end := start.AddDate(0, 0, 3)
		perDay := map[string]int{}
		for at := s.Next(start); !at.IsZero() && at.Before(end); at = s.Next(at) {
			perDay[at.Format("2006-01-02")]++
		}
		if len(perDay) != 3 {
			t.Fatalf("days = %v, want 3", perDay)
		}
		for day, n := range perDay {
			if n != 1 {
				t.Fatalf("%s fired %d times, want 1", day, n)
			}
		}
	}
}

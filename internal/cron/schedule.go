package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed 5-field cron expression evaluated in loc.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
	loc                           *time.Location
}

// ParseSchedule parses "minute hour day-of-month month day-of-week"; fields
// accept *, n, a-b, /step and comma lists (day-of-week 7 = Sunday).
func ParseSchedule(expr string, loc *time.Location) (Schedule, error) {
	if loc == nil {
		loc = time.Local
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("cron: schedule %q: want 5 fields, got %d", expr, len(f))
	}
	s := Schedule{loc: loc, domStar: f[2] == "*", dowStar: f[4] == "*"}
	for i, fld := range []struct {
		name   string
		lo, hi int
		dst    *uint64
	}{
		{"minute", 0, 59, &s.minute},
		{"hour", 0, 23, &s.hour},
		{"day-of-month", 1, 31, &s.dom},
		{"month", 1, 12, &s.month},
		{"day-of-week", 0, 7, &s.dow},
	} {
		v, err := fieldBits(f[i], fld.lo, fld.hi)
		if err != nil {
			return Schedule{}, fmt.Errorf("cron: %q %s: %w", expr, fld.name, err)
		}
		*fld.dst = v
	}
	if s.dow&(1<<7) != 0 {
		s.dow = s.dow&^(1<<7) | 1
	}
	return s, nil
}

func fieldBits(field string, lo, hi int) (uint64, error) {
	var out uint64
	for part := range strings.SplitSeq(field, ",") {
		a, b, step, err := fieldRange(part, lo, hi)
		if err != nil {
			return 0, err
		}
		for v := a; v <= b; v += step {
			out |= 1 << uint(v)
		}
	}
	return out, nil
}

func fieldRange(part string, lo, hi int) (a, b, step int, err error) {
	step = 1
	if base, st, ok := strings.Cut(part, "/"); ok {
		if step, err = strconv.Atoi(st); err != nil || step < 1 {
			return 0, 0, 0, fmt.Errorf("bad step %q", st)
		}
		part = base
	}
	if part == "*" {
		return lo, hi, step, nil
	}
	first, last, ranged := strings.Cut(part, "-")
	a, err = strconv.Atoi(first)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("bad value %q", first)
	}
	b = a
	if ranged {
		if b, err = strconv.Atoi(last); err != nil {
			return 0, 0, 0, fmt.Errorf("bad value %q", last)
		}
	}
	if a < lo || b > hi || a > b {
		return 0, 0, 0, fmt.Errorf("value %d-%d outside %d-%d", a, b, lo, hi)
	}
	return a, b, step, nil
}

// Next returns the first matching minute after t, zero if none in 5 years.
func (s Schedule) Next(t time.Time) time.Time {
	t = t.In(s.loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case s.month&(1<<uint(t.Month())) == 0:
			t = later(t, time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, s.loc))
		case !s.dayMatches(t):
			t = later(t, time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, s.loc))
		case s.hour&(1<<uint(t.Hour())) == 0:
			// Jump to the next scheduled hour; a wall-clock target can fall
			// in a DST gap (02:00 → 01:00 in New York), where time.Date
			// normalizes backwards and the naive loop would never advance.
			// The gap slot fires at the first existing minute after it.
			target := t.Hour() + 1
			if s.hour&(1<<uint(target)) != 0 {
				if m, ok := s.nextMinute(); ok {
					cand := time.Date(t.Year(), t.Month(), t.Day(), target, m, 0, 0, s.loc)
					if !cand.After(t) {
						cand = time.Date(t.Year(), t.Month(), t.Day(), target+1, m, 0, 0, s.loc)
					}
					if cand.After(t) {
						return cand
					}
				}
			}
			t = later(t, time.Date(t.Year(), t.Month(), t.Day(), target, 0, 0, 0, s.loc))
		case s.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// nextMinute returns the earliest minute bit set in the schedule.
func (s Schedule) nextMinute() (int, bool) {
	for m := range 60 {
		if s.minute&(1<<uint(m)) != 0 {
			return m, true
		}
	}
	return 0, false
}

// later guarantees forward progress: a DST gap may normalize a constructed
// time before t, and the caller must never revisit the same minute.
func later(t, cand time.Time) time.Time {
	if cand.After(t) {
		return cand
	}
	return t.Add(time.Minute)
}

func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	if s.domStar || s.dowStar {
		return dom && dow
	}
	return dom || dow
}

// Slots lists the matching minutes in (from, to], newest last, bounded.
func (s Schedule) Slots(from, to time.Time) []time.Time {
	var out []time.Time
	for t := s.Next(from); !t.IsZero() && !t.After(to) && len(out) < 1000; t = s.Next(t) {
		out = append(out, t)
	}
	return out
}

// Package timeutil holds the few time helpers we use across the booking layer.
// Two non-obvious things to remember:
//
//   - All storage uses UTC (INV-2). Display/input TZ conversion is per-edge.
//   - Slot iteration walks by minutes, not by hours/days, so DST transitions
//     produce the correct real-time slot length.
package timeutil

import (
	"errors"
	"time"
)

// ParseIANA returns the time.Location for a validated IANA name.
func ParseIANA(name string) (*time.Location, error) {
	if name == "" {
		return nil, errors.New("timezone required")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	return loc, nil
}

// ParseHM parses a "HH:MM" string into hours+minutes-of-day. 0 ≤ h ≤ 23, 0 ≤ m ≤ 59.
func ParseHM(s string) (h, m int, err error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, errors.New("expected HH:MM")
	}
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}

// Interval is a half-open [Start, End) window in UTC.
type Interval struct {
	Start, End time.Time
}

// Overlaps reports whether two half-open intervals share any instant.
func (i Interval) Overlaps(o Interval) bool {
	return i.Start.Before(o.End) && o.Start.Before(i.End)
}

// Duration returns End-Start as a Duration. Negative if End < Start (caller bug).
func (i Interval) Duration() time.Duration {
	return i.End.Sub(i.Start)
}

// DayWindowUTC returns the [00:00, 24:00) interval of localDate in loc, expressed
// in UTC. Used to bucket overrides/availability per local day.
func DayWindowUTC(localDate time.Time, loc *time.Location) Interval {
	y, mo, d := localDate.In(loc).Date()
	start := time.Date(y, mo, d, 0, 0, 0, 0, loc).UTC()
	end := time.Date(y, mo, d+1, 0, 0, 0, 0, loc).UTC()
	return Interval{Start: start, End: end}
}

// WeekdayISO returns 0=Mon..6=Sun for a date interpreted in loc. Matches the
// spec in Doc 04 (availability.weekday).
func WeekdayISO(t time.Time, loc *time.Location) int {
	wd := t.In(loc).Weekday() // 0=Sun..6=Sat
	if wd == time.Sunday {
		return 6
	}
	return int(wd) - 1
}

// AlignDown returns t rounded down to the nearest stepMinutes boundary in loc.
// Useful for snapping slot starts onto a clean grid.
func AlignDown(t time.Time, stepMinutes int, loc *time.Location) time.Time {
	if stepMinutes <= 0 {
		return t
	}
	local := t.In(loc)
	minsOfDay := local.Hour()*60 + local.Minute()
	aligned := (minsOfDay / stepMinutes) * stepMinutes
	y, mo, d := local.Date()
	return time.Date(y, mo, d, aligned/60, aligned%60, 0, 0, loc).UTC()
}

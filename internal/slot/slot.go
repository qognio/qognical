// Package slot computes free booking slots from a host's availability rules,
// per-date overrides, the event-type configuration, and existing bookings.
//
// The whole module is pure: it takes plain inputs (no PocketBase types) and
// returns slots, which makes it cheap to test against the edge-case matrix
// in docs/planning/09 (TZ-1..9, AV-1..8) without spinning up a database.
//
// Storage is UTC throughout; the only places we touch host-local time are
// when interpreting the HH:MM strings on Availability rules and DateOverrides
// in the host's IANA timezone. Daylight-savings transitions therefore "just
// work" because we let time.Date() do the conversion.
package slot

import (
	"sort"
	"time"

	"github.com/qognio/qognical/internal/timeutil"
)

// EventType is the slot-relevant subset of the event_types record.
type EventType struct {
	DurationMin     int
	BufferBeforeMin int
	BufferAfterMin  int
	MinNoticeMin    int
	MaxHorizonDays  int
}

// WeekRule is one entry in the host's weekly availability. Times are
// HH:MM strings in the host's timezone.
type WeekRule struct {
	Weekday int    // 0=Mon..6=Sun (matches ISO; see timeutil.WeekdayISO)
	Start   string // HH:MM, host-local
	End     string // HH:MM, host-local
}

// OverrideType disambiguates DateOverride rows.
type OverrideType int

const (
	OverrideUnavailable OverrideType = iota
	OverrideCustomHours
)

// DateOverride is a per-date deviation from the weekly rule.
type DateOverride struct {
	Date  time.Time // any time on the date; we use only year-month-day in the host tz
	Type  OverrideType
	Start string // HH:MM (host-local), only for CustomHours
	End   string // HH:MM (host-local), only for CustomHours
}

// Slot is a free booking position in UTC.
type Slot struct {
	StartUTC time.Time
	EndUTC   time.Time
}

// Input bundles everything ComputeSlots needs. Pass it explicitly rather than
// growing positional arguments.
type Input struct {
	EventType    EventType
	HostTimezone string // IANA
	Availability []WeekRule
	Overrides    []DateOverride

	// Busy intervals in UTC. LocalBusy comes from the bookings table (active
	// statuses); ExternalBusy comes from CalendarProvider.FreeBusy. They are
	// treated identically by the algorithm but kept separate so callers can
	// surface diagnostic info.
	LocalBusy    []timeutil.Interval
	ExternalBusy []timeutil.Interval

	// Now anchors MinNotice + MaxHorizon. Passed explicitly for testability;
	// production caller uses time.Now().UTC().
	Now time.Time

	// From, To define the half-open query window in UTC. Slots outside are
	// dropped even if they would otherwise be free.
	From time.Time
	To   time.Time

	// StepMinutes is the grid for slot starts. Zero means "use DurationMin",
	// which produces non-overlapping back-to-back slots.
	StepMinutes int
}

// ComputeSlots returns all free slots that satisfy the input rules.
// Output is sorted ascending by StartUTC.
func ComputeSlots(in Input) ([]Slot, error) {
	loc, err := timeutil.ParseIANA(in.HostTimezone)
	if err != nil {
		return nil, err
	}

	step := in.StepMinutes
	if step <= 0 {
		step = in.EventType.DurationMin
	}
	if step <= 0 || in.EventType.DurationMin <= 0 {
		return nil, nil
	}

	dur := time.Duration(in.EventType.DurationMin) * time.Minute
	bufferBefore := time.Duration(in.EventType.BufferBeforeMin) * time.Minute
	bufferAfter := time.Duration(in.EventType.BufferAfterMin) * time.Minute
	minNotice := time.Duration(in.EventType.MinNoticeMin) * time.Minute

	earliest := in.Now.Add(minNotice)
	var latest time.Time
	if in.EventType.MaxHorizonDays > 0 {
		latest = in.Now.Add(time.Duration(in.EventType.MaxHorizonDays) * 24 * time.Hour)
	}

	// Pre-index overrides by host-local date string (YYYY-MM-DD).
	overrides := map[string]DateOverride{}
	for _, o := range in.Overrides {
		key := o.Date.In(loc).Format("2006-01-02")
		overrides[key] = o
	}

	// Walk the date range in the host's timezone — that's the unit overrides
	// and weekday rules are tied to. Convert each free window to UTC at the
	// boundary so DST is correct.
	out := []Slot{}
	startDate := in.From.In(loc)
	y, mo, d := startDate.Date()
	cursor := time.Date(y, mo, d, 0, 0, 0, 0, loc)

	endDate := in.To.In(loc)
	for !cursor.After(endDate) {
		windows := freeWindowsForDay(cursor, in.Availability, overrides, loc)
		for _, w := range windows {
			out = append(out, candidatesInWindow(w, in, step, dur, bufferBefore, bufferAfter, earliest, latest)...)
		}
		cursor = cursor.AddDate(0, 0, 1)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].StartUTC.Before(out[j].StartUTC) })
	return out, nil
}

// freeWindowsForDay returns the [start,end) UTC intervals during which a host
// is *potentially* available on the given local day, without yet accounting
// for buffers or busy times. Override > weekly rule.
func freeWindowsForDay(localDay time.Time, weekly []WeekRule, overrides map[string]DateOverride, loc *time.Location) []timeutil.Interval {
	key := localDay.Format("2006-01-02")
	if ov, ok := overrides[key]; ok {
		switch ov.Type {
		case OverrideUnavailable:
			return nil
		case OverrideCustomHours:
			if iv, ok := windowFromHM(localDay, ov.Start, ov.End, loc); ok {
				return []timeutil.Interval{iv}
			}
			return nil
		}
	}
	wd := timeutil.WeekdayISO(localDay, loc)
	var windows []timeutil.Interval
	for _, r := range weekly {
		if r.Weekday != wd {
			continue
		}
		if iv, ok := windowFromHM(localDay, r.Start, r.End, loc); ok {
			windows = append(windows, iv)
		}
	}
	return mergeOverlaps(windows)
}

// windowFromHM converts a single HH:MM range on a local day into a UTC interval.
// Returns (_, false) for unparsable input or zero-length ranges.
func windowFromHM(localDay time.Time, startHM, endHM string, loc *time.Location) (timeutil.Interval, bool) {
	sh, sm, err := timeutil.ParseHM(startHM)
	if err != nil {
		return timeutil.Interval{}, false
	}
	eh, em, err := timeutil.ParseHM(endHM)
	if err != nil {
		return timeutil.Interval{}, false
	}
	y, mo, d := localDay.Date()
	start := time.Date(y, mo, d, sh, sm, 0, 0, loc).UTC()
	end := time.Date(y, mo, d, eh, em, 0, 0, loc).UTC()
	if !end.After(start) {
		return timeutil.Interval{}, false
	}
	return timeutil.Interval{Start: start, End: end}, true
}

// mergeOverlaps reduces overlapping intervals to their union (AV-4).
func mergeOverlaps(in []timeutil.Interval) []timeutil.Interval {
	if len(in) < 2 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Start.Before(in[j].Start) })
	out := []timeutil.Interval{in[0]}
	for _, iv := range in[1:] {
		last := &out[len(out)-1]
		if !iv.Start.After(last.End) {
			if iv.End.After(last.End) {
				last.End = iv.End
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

// candidatesInWindow enumerates slot starts on the step grid inside w that
// pass every check: query bounds, MinNotice, MaxHorizon, buffer-extended
// non-overlap with all busy intervals.
func candidatesInWindow(
	w timeutil.Interval,
	in Input,
	step int,
	dur, bufferBefore, bufferAfter time.Duration,
	earliest, latest time.Time,
) []Slot {
	loc, _ := timeutil.ParseIANA(in.HostTimezone)
	cursor := timeutil.AlignDown(w.Start, step, loc)
	if cursor.Before(w.Start) {
		cursor = cursor.Add(time.Duration(step) * time.Minute)
	}
	stepDur := time.Duration(step) * time.Minute

	var slots []Slot
	for {
		end := cursor.Add(dur)
		if end.After(w.End) {
			break
		}
		// TZ-7/8: "≥" allowed; "<" rejected.
		if !cursor.Before(earliest) && (latest.IsZero() || !cursor.After(latest)) {
			// Apply query window.
			if !cursor.Before(in.From) && cursor.Before(in.To) {
				if !busyConflict(cursor, end, bufferBefore, bufferAfter, in.LocalBusy, in.ExternalBusy) {
					slots = append(slots, Slot{StartUTC: cursor, EndUTC: end})
				}
			}
		}
		cursor = cursor.Add(stepDur)
	}
	return slots
}

func busyConflict(start, end time.Time, bufBefore, bufAfter time.Duration, sets ...[]timeutil.Interval) bool {
	extended := timeutil.Interval{Start: start.Add(-bufBefore), End: end.Add(bufAfter)}
	for _, set := range sets {
		for _, busy := range set {
			if extended.Overlaps(busy) {
				return true
			}
		}
	}
	return false
}

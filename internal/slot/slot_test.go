package slot

import (
	"testing"
	"time"

	"github.com/qognio/qognical/internal/timeutil"
)

func berlin() *time.Location {
	loc, _ := time.LoadLocation("Europe/Berlin")
	return loc
}

func mustParse(t *testing.T, layout, s string) time.Time {
	tm, err := time.Parse(layout, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// Baseline: 30-min event, Mon 09:00-12:00 availability, no busy → 6 slots.
func TestBasicSlots(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc) // Mon
	in := Input{
		EventType:    EventType{DurationMin: 30, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 0, Start: "09:00", End: "12:00"}},
		Now:          time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From:         mon.UTC(),
		To:           mon.AddDate(0, 0, 1).UTC(),
	}
	got, err := ComputeSlots(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d slots, want 6", len(got))
	}
	if got[0].StartUTC.In(loc).Hour() != 9 {
		t.Errorf("first slot local hour = %d, want 9", got[0].StartUTC.In(loc).Hour())
	}
}

// AV-1: no availability rules → empty.
func TestNoAvailability(t *testing.T) {
	loc := berlin()
	in := Input{
		EventType:    EventType{DurationMin: 30, MaxHorizonDays: 7},
		HostTimezone: "Europe/Berlin",
		Now:          time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From:         time.Date(2026, 6, 1, 0, 0, 0, 0, loc).UTC(),
		To:           time.Date(2026, 6, 8, 0, 0, 0, 0, loc).UTC(),
	}
	got, _ := ComputeSlots(in)
	if len(got) != 0 {
		t.Errorf("expected 0 slots, got %d", len(got))
	}
}

// AV-4: overlapping availability rules → union, no duplicates.
func TestOverlappingRulesUnion(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 60, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{
			{Weekday: 0, Start: "09:00", End: "11:00"},
			{Weekday: 0, Start: "10:00", End: "12:00"},
		},
		Now:  time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From: mon.UTC(),
		To:   mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	if len(got) != 3 { // 9-10, 10-11, 11-12
		t.Errorf("expected 3 union slots, got %d", len(got))
	}
}

// AV-5: date override "unavailable" suppresses the day entirely.
func TestOverrideUnavailable(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 30, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 0, Start: "09:00", End: "12:00"}},
		Overrides:    []DateOverride{{Date: mon, Type: OverrideUnavailable}},
		Now:          time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From:         mon.UTC(),
		To:           mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	if len(got) != 0 {
		t.Errorf("override-unavailable should yield 0, got %d", len(got))
	}
}

// AV-6: custom_hours override replaces the weekly rule.
func TestOverrideCustomHoursWins(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 60, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 0, Start: "09:00", End: "12:00"}}, // ignored
		Overrides:    []DateOverride{{Date: mon, Type: OverrideCustomHours, Start: "14:00", End: "16:00"}},
		Now:          time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From:         mon.UTC(),
		To:           mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	if len(got) != 2 { // 14-15, 15-16
		t.Errorf("custom_hours override yielded %d slots, want 2", len(got))
	}
	if h := got[0].StartUTC.In(loc).Hour(); h != 14 {
		t.Errorf("first slot hour = %d, want 14", h)
	}
}

// AV-7: event-type duration longer than the window → 0 slots.
func TestDurationExceedsWindow(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 120, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 0, Start: "09:00", End: "10:30"}},
		Now:          time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From:         mon.UTC(),
		To:           mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	if len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}

// AV-8 + TZ-7: slots before now+minNotice are filtered.
func TestMinNoticeExclusion(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 30, MinNoticeMin: 120, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 0, Start: "09:00", End: "12:00"}},
		// Now is 10:30 local; with 120min notice, earliest slot start = 12:30 → after the window.
		Now:  time.Date(2026, 6, 1, 10, 30, 0, 0, loc).UTC(),
		From: mon.UTC(),
		To:   mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	if len(got) != 0 {
		t.Errorf("min_notice should suppress all, got %d", len(got))
	}
}

// CR-1 prerequisite: local busy interval blocks overlapping slot.
func TestLocalBusyBlocks(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 30, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 0, Start: "09:00", End: "12:00"}},
		LocalBusy: []timeutil.Interval{
			{Start: time.Date(2026, 6, 1, 10, 0, 0, 0, loc).UTC(), End: time.Date(2026, 6, 1, 10, 30, 0, 0, loc).UTC()},
		},
		Now:  time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From: mon.UTC(),
		To:   mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	// expected: 9-9:30, 9:30-10, [10-10:30 blocked], 10:30-11, 11-11:30, 11:30-12 = 5
	if len(got) != 5 {
		t.Errorf("got %d, want 5 (one busy)", len(got))
	}
}

// Buffer extends both directions.
func TestBufferExtendsBusyCheck(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 30, BufferBeforeMin: 15, BufferAfterMin: 15, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 0, Start: "09:00", End: "12:00"}},
		LocalBusy: []timeutil.Interval{
			{Start: time.Date(2026, 6, 1, 10, 0, 0, 0, loc).UTC(), End: time.Date(2026, 6, 1, 10, 30, 0, 0, loc).UTC()},
		},
		Now:  time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From: mon.UTC(),
		To:   mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	// 9:30-10 has buffer-after running into 10:15 (overlap with 10-10:30 busy) → blocked.
	// 10:30-11 has buffer-before running into 10:15 (overlap) → blocked.
	// Expected: 9-9:30, 11-11:30, 11:30-12 = 3 slots
	if len(got) != 3 {
		t.Errorf("buffer should block neighbours: got %d slots, want 3", len(got))
	}
}

// TZ-3: slot crossing midnight LOCAL. Availability rule 23:00-24:00 produces
// one slot (23:00-23:30) when duration=30. Slots never bridge midnight unless
// the rule itself does.
func TestSlotsAcrossMidnightAreNotBridged(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 30, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 0, Start: "23:00", End: "23:59"}},
		Now:          time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From:         mon.UTC(),
		To:           mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	if len(got) != 1 {
		t.Errorf("near-midnight produced %d, want 1", len(got))
	}
}

// TZ-2: invitee in different TZ — we return UTC, display is caller concern.
// Sanity check that UTC is returned regardless of host TZ.
func TestOutputIsUTC(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 60, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 0, Start: "10:00", End: "11:00"}},
		Now:          time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From:         mon.UTC(),
		To:           mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	if len(got) != 1 || got[0].StartUTC.Location() != time.UTC {
		t.Errorf("output not UTC")
	}
}

// MaxHorizon boundary.
func TestMaxHorizon(t *testing.T) {
	loc := berlin()
	day := time.Date(2026, 6, 5, 0, 0, 0, 0, loc) // Fri, 4 days after now
	in := Input{
		EventType:    EventType{DurationMin: 30, MaxHorizonDays: 3}, // only 3 days from now allowed
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{{Weekday: 4, Start: "09:00", End: "10:00"}}, // Fri
		Now:          time.Date(2026, 6, 1, 0, 0, 0, 0, loc).UTC(),
		From:         day.UTC(),
		To:           day.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	if len(got) != 0 {
		t.Errorf("beyond max_horizon should be empty, got %d", len(got))
	}
}

// Smoke: ensure deterministic order.
func TestOrderAscending(t *testing.T) {
	loc := berlin()
	mon := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	in := Input{
		EventType:    EventType{DurationMin: 30, MaxHorizonDays: 30},
		HostTimezone: "Europe/Berlin",
		Availability: []WeekRule{
			{Weekday: 0, Start: "14:00", End: "15:00"},
			{Weekday: 0, Start: "09:00", End: "10:00"},
		},
		Now:  time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
		From: mon.UTC(),
		To:   mon.AddDate(0, 0, 1).UTC(),
	}
	got, _ := ComputeSlots(in)
	for i := 1; i < len(got); i++ {
		if got[i].StartUTC.Before(got[i-1].StartUTC) {
			t.Errorf("output not sorted at i=%d", i)
		}
	}
	// avoid unused-import warning by referencing the constant somewhere.
	_ = mustParse
}

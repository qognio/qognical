package timeutil

import (
	"testing"
	"time"
)

func TestOverlapsHalfOpen(t *testing.T) {
	a := Interval{
		Start: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC),
	}
	// Back-to-back must not overlap.
	b := Interval{Start: a.End, End: a.End.Add(time.Hour)}
	if a.Overlaps(b) {
		t.Errorf("back-to-back intervals should not overlap")
	}
	// Touching by 1 minute inside.
	c := Interval{Start: a.End.Add(-time.Minute), End: a.End.Add(time.Hour)}
	if !a.Overlaps(c) {
		t.Errorf("1-minute overlap should be detected")
	}
}

// TZ-1: DST transition mid-slot — physical minute count must be preserved.
// Berlin DST forward: 2026-03-29 02:00 local → 03:00 local (loses an hour).
func TestDSTMidSlotKeepsDuration(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	// Slot from 01:30 to 02:00 local on DST day: the clock skips ahead at 02:00
	// so the next 30-min window 02:00-02:30 doesn't physically exist; the
	// caller would either schedule outside or after 03:00.
	// We just verify Interval.Duration matches the UTC delta:
	start := time.Date(2026, 3, 29, 1, 30, 0, 0, loc).UTC()
	end := time.Date(2026, 3, 29, 4, 0, 0, 0, loc).UTC()
	got := Interval{Start: start, End: end}.Duration()
	want := 90 * time.Minute // 1:30 local→4:00 local crosses the spring-forward, real-time = 1h30m
	if got != want {
		t.Errorf("DST-aware duration = %v, want %v", got, want)
	}
}

// TZ-5: availability across midnight (22:00–02:00) — translated by caller to two
// intervals or a single one bridging midnight. We don't enforce here, just
// expose the building blocks.
func TestDayWindowUTC(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	local := time.Date(2026, 6, 1, 14, 30, 0, 0, loc) // any time on that day
	w := DayWindowUTC(local, loc)
	if w.Duration() != 24*time.Hour {
		t.Errorf("day window len = %v, want 24h", w.Duration())
	}
}

func TestWeekdayISO(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	cases := []struct {
		date    time.Time
		wantISO int
	}{
		{time.Date(2026, 6, 1, 12, 0, 0, 0, loc), 0}, // Mon
		{time.Date(2026, 6, 7, 12, 0, 0, 0, loc), 6}, // Sun
	}
	for _, c := range cases {
		got := WeekdayISO(c.date, loc)
		if got != c.wantISO {
			t.Errorf("WeekdayISO(%v) = %d, want %d", c.date, got, c.wantISO)
		}
	}
}

func TestParseHM(t *testing.T) {
	h, m, err := ParseHM("09:15")
	if err != nil || h != 9 || m != 15 {
		t.Errorf("got %d:%d err=%v", h, m, err)
	}
	if _, _, err := ParseHM("9:00"); err == nil {
		t.Errorf("expected error on short input")
	}
}

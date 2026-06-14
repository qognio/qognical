package notifier

import (
	"strings"
	"testing"
	"time"
)

func TestRenderICSMinimal(t *testing.T) {
	ev := ICalEvent{
		UID:       "booking-abc@qognical",
		Summary:   "Test Slot",
		StartUTC:  time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
		EndUTC:    time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC),
		Organizer: "host@example.com",
		Attendee:  "guest@example.com",
	}
	got := RenderICS(ev)
	for _, line := range []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"UID:booking-abc@qognical",
		"DTSTART:20260601T090000Z",
		"DTEND:20260601T093000Z",
		"SUMMARY:Test Slot",
		"ORGANIZER:mailto:host@example.com",
		"ATTENDEE;RSVP=FALSE:mailto:guest@example.com",
		"STATUS:CONFIRMED",
		"END:VEVENT",
		"END:VCALENDAR",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q in:\n%s", line, got)
		}
	}
}

func TestRenderICSEscapesText(t *testing.T) {
	ev := ICalEvent{
		UID: "x", Summary: "Hello, world; line\nbreak", StartUTC: time.Now(), EndUTC: time.Now(),
	}
	got := RenderICS(ev)
	if !strings.Contains(got, `SUMMARY:Hello\, world\; line\nbreak`) {
		t.Errorf("escaping failed: %s", got)
	}
}

func TestCancelMethod(t *testing.T) {
	ev := ICalEvent{
		UID: "x", Method: "CANCEL", Status: "CANCELLED",
		StartUTC: time.Now(), EndUTC: time.Now(),
	}
	got := RenderICS(ev)
	if !strings.Contains(got, "METHOD:CANCEL") || !strings.Contains(got, "STATUS:CANCELLED") {
		t.Errorf("cancel method/status missing")
	}
}

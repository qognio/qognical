// Package notifier renders booking notification emails and the iCalendar
// attachment that goes with them. The iCal generator is split out as a pure
// function to keep it unit-testable without SMTP.
package notifier

import (
	"fmt"
	"strings"
	"time"
)

// ICalEvent is the minimum we need to emit a RFC 5545 VEVENT.
type ICalEvent struct {
	UID         string
	Summary     string
	Description string
	Location    string
	StartUTC    time.Time
	EndUTC      time.Time
	Organizer   string // mailto: target
	Attendee    string // mailto: target
	Sequence    int    // 0 for initial, +1 for each reschedule
	Method      string // PUBLISH, REQUEST, CANCEL
	Status      string // CONFIRMED, CANCELLED
}

// RenderICS emits a tiny RFC 5545 document. Tested against Apple Calendar,
// Google Calendar, and Outlook in practice — the format is conservative.
func RenderICS(e ICalEvent) string {
	if e.Method == "" {
		e.Method = "PUBLISH"
	}
	if e.Status == "" {
		e.Status = "CONFIRMED"
	}
	now := time.Now().UTC()
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//Qognical//Booking//EN\r\n")
	b.WriteString("CALSCALE:GREGORIAN\r\n")
	fmt.Fprintf(&b, "METHOD:%s\r\n", e.Method)
	b.WriteString("BEGIN:VEVENT\r\n")
	fmt.Fprintf(&b, "UID:%s\r\n", e.UID)
	fmt.Fprintf(&b, "SEQUENCE:%d\r\n", e.Sequence)
	fmt.Fprintf(&b, "DTSTAMP:%s\r\n", fmtUTC(now))
	fmt.Fprintf(&b, "DTSTART:%s\r\n", fmtUTC(e.StartUTC))
	fmt.Fprintf(&b, "DTEND:%s\r\n", fmtUTC(e.EndUTC))
	fmt.Fprintf(&b, "SUMMARY:%s\r\n", escape(e.Summary))
	if e.Description != "" {
		fmt.Fprintf(&b, "DESCRIPTION:%s\r\n", escape(e.Description))
	}
	if e.Location != "" {
		fmt.Fprintf(&b, "LOCATION:%s\r\n", escape(e.Location))
	}
	if e.Organizer != "" {
		fmt.Fprintf(&b, "ORGANIZER:mailto:%s\r\n", e.Organizer)
	}
	if e.Attendee != "" {
		// For METHOD:REQUEST the attendee must be an actionable participant so
		// mail clients (Gmail/Outlook/Apple) render Yes/Maybe/No RSVP buttons.
		// PUBLISH/CANCEL keep the passive form.
		if e.Method == "REQUEST" {
			fmt.Fprintf(&b, "ATTENDEE;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:%s\r\n", e.Attendee)
		} else {
			fmt.Fprintf(&b, "ATTENDEE;RSVP=FALSE:mailto:%s\r\n", e.Attendee)
		}
	}
	fmt.Fprintf(&b, "STATUS:%s\r\n", e.Status)
	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

func fmtUTC(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// escape applies RFC 5545 text escaping. Order matters: backslash first.
func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

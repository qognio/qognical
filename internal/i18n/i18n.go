// Package i18n holds the tiny label maps for the booking SPA and email
// templates. We deliberately don't pull in a real i18n framework (per the
// anti-goal in docs/planning/01) — two static maps in code are enough for
// the DE/EN scope of v1.1.
//
// Adding a third language = adding a third map; the SPA pulls these via
// /api/public/v1/labels which serves the JSON below verbatim.
package i18n

// Labels is the user-facing string map per locale. Keys are stable.
type Labels map[string]string

var de = Labels{
	"loading":           "Wird geladen…",
	"no_slots":          "Keine freien Termine im gewählten Zeitraum.",
	"prev_week":         "← Vorherige Woche",
	"next_week":         "Nächste Woche →",
	"your_details":      "Deine Daten",
	"name":              "Name",
	"email":             "E-Mail",
	"book_slot":         "Diesen Termin buchen",
	"booking_confirmed": "Buchung bestätigt",
	"sent_to":           "Bestätigung an {email}",
	"manage_link":       "Termin verwalten",
	"awaiting_approval": "Anfrage gesendet — wartet auf Bestätigung",
	"approval_pending":  "{host} prüft deine Anfrage. Du bekommst eine E-Mail sobald entschieden wurde.",
	"booking_failed":    "Buchung fehlgeschlagen",
	"slot_taken":        "Dieser Termin ist nicht mehr verfügbar — bitte einen anderen wählen.",
	"capacity_full":     "Dieser Slot ist ausgebucht.",
	"reschedule":        "Verschieben",
	"cancel":            "Stornieren",
	"reason_optional":   "Begründung (optional)",
	"really_cancel":     "Buchung wirklich stornieren?",
	"cancelled_heading": "Buchung storniert",
	"cancelled_body":    "Diese Buchung wurde abgesagt.",
	"new_start_label":   "Neuer Termin:",
	"duration_min":      "{n} Min",
	"with_host":         "mit {host}",
	"times_in_tz":       "Zeiten in {tz}",
}

var en = Labels{
	"loading":           "Loading…",
	"no_slots":          "No free slots in this range.",
	"prev_week":         "← Prev week",
	"next_week":         "Next week →",
	"your_details":      "Your details",
	"name":              "Name",
	"email":             "Email",
	"book_slot":         "Book this slot",
	"booking_confirmed": "Booking confirmed",
	"sent_to":           "Confirmation sent to {email}",
	"manage_link":       "Manage booking",
	"awaiting_approval": "Request sent — waiting for confirmation",
	"approval_pending":  "{host} is reviewing your request. You'll get an email once they decide.",
	"booking_failed":    "Booking failed",
	"slot_taken":        "This slot is no longer available — please pick another.",
	"capacity_full":     "This slot is fully booked.",
	"reschedule":        "Reschedule",
	"cancel":            "Cancel",
	"reason_optional":   "Reason (optional)",
	"really_cancel":     "Really cancel this booking?",
	"cancelled_heading": "Booking cancelled",
	"cancelled_body":    "This booking has been cancelled.",
	"new_start_label":   "New start time:",
	"duration_min":      "{n} min",
	"with_host":         "with {host}",
	"times_in_tz":       "times shown in {tz}",
}

// All returns the full map keyed by locale code. Used by the /labels endpoint.
func All() map[string]Labels {
	return map[string]Labels{"de": de, "en": en}
}

// For returns the requested locale, falling back to DE → EN.
func For(locale string) Labels {
	switch locale {
	case "en":
		return en
	}
	return de
}

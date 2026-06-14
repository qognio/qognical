package notifier

// All templates kept inline for now; Phase 2 anti-goal is over-engineering a
// theming system. Phase 1.1 can lift these to a settings collection with
// per-instance overrides.

const tmplConfirmationInvitee = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Buchung bestätigt</h2>
<p>Hallo {{.InviteeName}},</p>
<p>Dein Termin <strong>{{.EventTypeTitle}}</strong> mit {{.HostName}} ist bestätigt:</p>
<p><strong>{{.LocalStart}}</strong></p>
{{if .MeetingURL}}<p>Meeting-Link: <a href="{{.MeetingURL}}">{{.MeetingURL}}</a></p>{{end}}
<p>Du kannst die Buchung jederzeit über folgenden Link verwalten (verschieben oder absagen):</p>
<p><a href="{{.ManageURL}}">{{.ManageURL}}</a></p>
<p>Eine Kalendereinladung (booking.ics) findest du im Anhang.</p>
</body></html>`

const tmplConfirmationHost = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Neue Buchung</h2>
<p>{{.InviteeName}} ({{.InviteeEmail}}) hat den Slot <strong>{{.EventTypeTitle}}</strong> am
<strong>{{.LocalStart}}</strong> gebucht.</p>
{{if .MeetingURL}}<p>Meeting: {{.MeetingURL}}</p>{{end}}
</body></html>`

const tmplCancelInvitee = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Buchung storniert</h2>
<p>Hallo {{.InviteeName}},</p>
<p>Deine Buchung <strong>{{.EventTypeTitle}}</strong> für
<strong>{{.LocalStart}}</strong> ist storniert.</p>
<p>Eine neue Buchung kannst du jederzeit unter {{.BaseURL}} anlegen.</p>
</body></html>`

const tmplCancelHost = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Storniert: {{.EventTypeTitle}}</h2>
<p>{{.InviteeName}} hat den Termin am <strong>{{.LocalStart}}</strong> abgesagt.</p>
</body></html>`

const tmplRescheduleInvitee = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Buchung verschoben</h2>
<p>Hallo {{.InviteeName}},</p>
<p>Dein Termin <strong>{{.EventTypeTitle}}</strong> wurde auf
<strong>{{.LocalStart}}</strong> verschoben.</p>
{{if .MeetingURL}}<p>Meeting: <a href="{{.MeetingURL}}">{{.MeetingURL}}</a></p>{{end}}
<p>Verwalten: <a href="{{.ManageURL}}">{{.ManageURL}}</a></p>
</body></html>`

const tmplRescheduleHost = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Verschoben: {{.EventTypeTitle}}</h2>
<p>{{.InviteeName}} hat den Termin auf <strong>{{.LocalStart}}</strong> verschoben.</p>
</body></html>`

const tmplApprovalRequestHost = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Neue Buchungs-Anfrage</h2>
<p>{{.InviteeName}} ({{.InviteeEmail}}) möchte <strong>{{.EventTypeTitle}}</strong>
am <strong>{{.LocalStart}}</strong> buchen.</p>
<p style="margin-top:24px;">
  <a href="{{.ManageURL}}" style="display:inline-block;padding:10px 18px;background:#2B5ADC;color:#fff;border-radius:8px;text-decoration:none;font-weight:600;">Annehmen</a>
  &nbsp;
  <a href="{{.DeclineURL}}" style="display:inline-block;padding:10px 18px;background:#fff;color:#b91c1c;border:1px solid #b91c1c;border-radius:8px;text-decoration:none;font-weight:600;">Ablehnen</a>
</p>
<p>Die Anfrage gilt 7 Tage. Danach gilt sie automatisch als abgelaufen.</p>
</body></html>`

const tmplApprovalPendingInvitee = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Anfrage erhalten</h2>
<p>Hallo {{.InviteeName}},</p>
<p>deine Buchungs-Anfrage für <strong>{{.EventTypeTitle}}</strong> am
<strong>{{.LocalStart}}</strong> ist bei {{.HostName}} angekommen.</p>
<p>Sobald {{.HostName}} bestätigt oder ablehnt, erhältst du eine
weitere E-Mail.</p>
</body></html>`

const tmplApprovalCheckoutInvitee = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Bestätigt — letzter Schritt</h2>
<p>Hallo {{.InviteeName}},</p>
<p>{{.HostName}} hat deine Buchung <strong>{{.EventTypeTitle}}</strong> am
<strong>{{.LocalStart}}</strong> bestätigt.</p>
<p>Bitte schließe die Buchung mit der Zahlung ab:</p>
<p><a href="{{.ManageURL}}">{{.ManageURL}}</a></p>
</body></html>`

const tmplApprovalDeclinedInvitee = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Leider nicht möglich</h2>
<p>Hallo {{.InviteeName}},</p>
<p>{{.HostName}} kann deine Anfrage für <strong>{{.EventTypeTitle}}</strong>
am <strong>{{.LocalStart}}</strong> nicht bestätigen.</p>
{{if .Reason}}<p><em>Begründung: {{.Reason}}</em></p>{{end}}
<p>Du kannst gerne einen anderen Termin unter {{.BaseURL}} anfragen.</p>
</body></html>`

const tmplReminderInvitee = `<!doctype html>
<html><body style="font-family: system-ui, sans-serif; max-width: 560px;">
<h2>Erinnerung</h2>
<p>Hallo {{.InviteeName}},</p>
<p>In {{.Kind}} startet dein Termin <strong>{{.EventTypeTitle}}</strong> mit {{.HostName}}:</p>
<p><strong>{{.LocalStart}}</strong></p>
{{if .MeetingURL}}<p>Meeting: <a href="{{.MeetingURL}}">{{.MeetingURL}}</a></p>{{end}}
</body></html>`

package notifier

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/tools/mailer"

	"github.com/qognio/qognical/internal/config"
)

// Booking is the notifier-facing view of a booking record. We don't pull in
// PocketBase types here — the caller adapts.
type Booking struct {
	ID             string
	EventTypeTitle string
	HostName       string
	HostEmail      string
	InviteeName    string
	InviteeEmail   string
	StartUTC       time.Time
	EndUTC         time.Time
	InviteeTZ      string // IANA; used to render local time in the email
	ManageURL      string // includes signed token
	MeetingURL     string // empty if location_type is in_person / phone / none
	BaseURL        string
}

// Notifier sends booking-related emails via SMTP. Construct once at startup.
type Notifier struct {
	client mailer.Mailer
	from   mail.Address
	base   string
}

// New builds a notifier from the parsed configuration. The mailer is an
// `mailer.SMTPClient` configured from QOGNICAL_SMTP_*.
func New(cfg *config.Config) *Notifier {
	client := &mailer.SMTPClient{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		Username: cfg.SMTP.User,
		Password: cfg.SMTP.Password,
		// Use TLS on the standard ports (465 implicit, 587 STARTTLS via the
		// mailer's auto handling). The mailer auto-negotiates when this flag
		// is true. For local-dev smtp.test we still set it; tests stub the
		// mailer out at the Send-level.
		TLS: cfg.SMTP.Port == 465,
	}
	return &Notifier{
		client: client,
		from:   mail.Address{Name: "Qognical", Address: cfg.SMTP.From},
		base:   cfg.BaseURL,
	}
}

// WithMailer is for tests: swap in a fake mailer (e.g. a slice-collecting one).
func (n *Notifier) WithMailer(m mailer.Mailer) *Notifier {
	n.client = m
	return n
}

// SendConfirmation sends the invitee a "Buchung bestätigt" email with an
// iCal attachment of method PUBLISH/STATUS:CONFIRMED. Also pings the host
// in a separate email so they know to expect the meeting.
func (n *Notifier) SendConfirmation(b Booking) error {
	ics := RenderICS(b.toICalEvent("PUBLISH", "CONFIRMED", 0))
	body, err := render(tmplConfirmationInvitee, b)
	if err != nil {
		return err
	}
	if err := n.send(b.InviteeEmail, fmt.Sprintf("Buchung bestätigt: %s", b.EventTypeTitle), body, ics); err != nil {
		return err
	}
	// host copy (best effort; failure here doesn't block invitee)
	hostBody, _ := render(tmplConfirmationHost, b)
	_ = n.send(b.HostEmail, fmt.Sprintf("Neue Buchung: %s", b.EventTypeTitle), hostBody, ics)
	return nil
}

// SendCancellation tells both parties the booking is off, with a METHOD:CANCEL ICS.
func (n *Notifier) SendCancellation(b Booking) error {
	ics := RenderICS(b.toICalEvent("CANCEL", "CANCELLED", 1))
	body, err := render(tmplCancelInvitee, b)
	if err != nil {
		return err
	}
	if err := n.send(b.InviteeEmail, fmt.Sprintf("Buchung storniert: %s", b.EventTypeTitle), body, ics); err != nil {
		return err
	}
	hostBody, _ := render(tmplCancelHost, b)
	_ = n.send(b.HostEmail, fmt.Sprintf("Storniert: %s", b.EventTypeTitle), hostBody, ics)
	return nil
}

// SendReschedule announces the new slot.
func (n *Notifier) SendReschedule(b Booking, sequence int) error {
	ics := RenderICS(b.toICalEvent("REQUEST", "CONFIRMED", sequence))
	body, err := render(tmplRescheduleInvitee, b)
	if err != nil {
		return err
	}
	if err := n.send(b.InviteeEmail, fmt.Sprintf("Buchung verschoben: %s", b.EventTypeTitle), body, ics); err != nil {
		return err
	}
	hostBody, _ := render(tmplRescheduleHost, b)
	_ = n.send(b.HostEmail, fmt.Sprintf("Verschoben: %s", b.EventTypeTitle), hostBody, ics)
	return nil
}

// ----- v1.1 approval-workflow surfaces -----

// SendApprovalRequest emails the host with two CTAs: ManageURL is the
// approve-link, declineURL is rendered as the secondary action.
func (n *Notifier) SendApprovalRequest(b Booking, declineURL string) error {
	subject := fmt.Sprintf("Bestätigung benötigt: %s", b.EventTypeTitle)
	body, err := render(tmplApprovalRequestHost, struct {
		Booking
		DeclineURL string
	}{b, declineURL})
	if err != nil {
		return err
	}
	return n.send(b.HostEmail, subject, body, "")
}

// SendApprovalPending tells the invitee their request was received.
func (n *Notifier) SendApprovalPending(b Booking) error {
	body, err := render(tmplApprovalPendingInvitee, b)
	if err != nil {
		return err
	}
	return n.send(b.InviteeEmail,
		fmt.Sprintf("Anfrage erhalten: %s", b.EventTypeTitle), body, "")
}

// SendApprovalCheckout notifies the invitee that the host approved AND
// payment is now required — link in ManageURL.
func (n *Notifier) SendApprovalCheckout(b Booking) error {
	body, err := render(tmplApprovalCheckoutInvitee, b)
	if err != nil {
		return err
	}
	return n.send(b.InviteeEmail,
		fmt.Sprintf("Bestätigt — Zahlung benötigt: %s", b.EventTypeTitle), body, "")
}

// SendApprovalDeclined informs the invitee that the host declined.
func (n *Notifier) SendApprovalDeclined(b Booking, reason string) error {
	body, err := render(tmplApprovalDeclinedInvitee, struct {
		Booking
		Reason string
	}{b, reason})
	if err != nil {
		return err
	}
	return n.send(b.InviteeEmail,
		fmt.Sprintf("Leider nicht möglich: %s", b.EventTypeTitle), body, "")
}

// SendReminder is for the 24h/1h crons.
func (n *Notifier) SendReminder(b Booking, kind string) error {
	subject := fmt.Sprintf("Erinnerung: %s in %s", b.EventTypeTitle, kind)
	body, err := render(tmplReminderInvitee, struct {
		Booking
		Kind string
	}{b, kind})
	if err != nil {
		return err
	}
	return n.send(b.InviteeEmail, subject, body, "")
}

func (n *Notifier) send(to, subject, htmlBody, icsAttachment string) error {
	addr, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("bad recipient: %w", err)
	}
	msg := &mailer.Message{
		From:    n.from,
		To:      []mail.Address{*addr},
		Subject: subject,
		HTML:    htmlBody,
		Text:    stripHTML(htmlBody),
	}
	if icsAttachment != "" {
		msg.Attachments = map[string]io.Reader{
			"booking.ics": strings.NewReader(icsAttachment),
		}
	}
	return n.client.Send(msg)
}

func render(tpl string, data any) (string, error) {
	t, err := template.New("email").Parse(tpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// stripHTML is a tiny plaintext fallback for the Text part of the message.
// Good enough for our few transactional templates; not a real HTML parser.
func stripHTML(s string) string {
	var b strings.Builder
	skip := false
	for _, r := range s {
		switch r {
		case '<':
			skip = true
		case '>':
			skip = false
		default:
			if !skip {
				b.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func (b Booking) toICalEvent(method, status string, seq int) ICalEvent {
	desc := ""
	if b.MeetingURL != "" {
		desc = "Join: " + b.MeetingURL
	}
	loc := ""
	if b.MeetingURL != "" {
		loc = b.MeetingURL
	}
	return ICalEvent{
		UID:         fmt.Sprintf("booking-%s@qognical", b.ID),
		Summary:     b.EventTypeTitle + " mit " + b.HostName,
		Description: desc,
		Location:    loc,
		StartUTC:    b.StartUTC,
		EndUTC:      b.EndUTC,
		Organizer:   b.HostEmail,
		Attendee:    b.InviteeEmail,
		Sequence:    seq,
		Method:      method,
		Status:      status,
	}
}

// LocalStart formats StartUTC in the invitee's timezone for the templates.
func (b Booking) LocalStart() string {
	if b.InviteeTZ == "" {
		return b.StartUTC.Format("Mon 02 Jan 2006, 15:04 UTC")
	}
	loc, err := time.LoadLocation(b.InviteeTZ)
	if err != nil {
		return b.StartUTC.Format(time.RFC1123)
	}
	return b.StartUTC.In(loc).Format("Mon 02 Jan 2006, 15:04 MST")
}

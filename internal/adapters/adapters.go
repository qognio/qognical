// Package adapters defines the three provider-family interfaces the booking
// pipeline talks to (Calendar, Meeting, Payment). Each concrete provider lives
// in its own subpackage and is registered via Registry at startup.
//
// Design principle (per project ADR-disposition): no heavy SDK dependencies.
// Every adapter uses net/http + encoding/json directly. Trades a bit of
// boilerplate for a small image, no transitive CVE surface, and code we can
// actually read.
package adapters

import (
	"context"
	"errors"
	"time"

	"github.com/qognio/qognical/internal/timeutil"
)

// ErrUnavailable signals a transient provider problem (5xx, network, rate
// limit). The pipeline treats this as "do later", keeps the booking local,
// and lets the retry-cron pick it up.
var ErrUnavailable = errors.New("provider unavailable")

// ErrAuth signals the provider rejected our credentials. The host needs to
// re-authorise (OAuth refresh failed, app password revoked, etc.).
var ErrAuth = errors.New("provider auth failed")

// ----- Calendar -----

type CalendarProvider interface {
	// FreeBusy returns busy intervals for the host between from and to.
	// Used as input to slot.ComputeSlots when a calendar integration is
	// configured. Should never return the contents of events, just busy
	// windows (privacy / Doc 07 INV-7).
	FreeBusy(ctx context.Context, from, to time.Time) ([]timeutil.Interval, error)

	// CreateEvent persists an event in the external calendar. Returns the
	// provider-side event ID so we can update/delete later.
	CreateEvent(ctx context.Context, in CalendarEvent) (CreatedEvent, error)

	// UpdateEvent rewrites times / metadata of an existing external event.
	UpdateEvent(ctx context.Context, externalID string, in CalendarEvent) error

	// DeleteEvent removes the external event. Idempotent: deleting an
	// already-gone event must return nil.
	DeleteEvent(ctx context.Context, externalID string) error

	// Name returns the provider identifier for logs (msgraph/nextcloud/google).
	Name() string
}

// CalendarEvent is the provider-agnostic event payload.
type CalendarEvent struct {
	Summary       string
	Description   string
	Location      string
	StartUTC      time.Time
	EndUTC        time.Time
	OrganizerMail string
	AttendeeName  string
	AttendeeMail  string
	// CreateOnlineMeeting hints to a provider that combines calendar + meeting
	// in one step (MS Graph teamsForBusiness — see docs/planning/05 Weg 1).
	CreateOnlineMeeting bool
}

// CreatedEvent carries IDs the pipeline persists in bookings.*.
type CreatedEvent struct {
	ExternalID string
	MeetingURL string // populated when CreateOnlineMeeting was requested
	MeetingID  string // optional, provider-specific
}

// ----- Meeting -----

type MeetingProvider interface {
	// CreateMeeting returns a join URL for the booking. Some providers are
	// stateless (Jitsi) — the URL is just generated; others (Teams Weg 2)
	// would talk to an API. The pipeline calls this when the event-type's
	// location_type maps to a meeting provider and the calendar adapter
	// didn't already create one.
	CreateMeeting(ctx context.Context, in MeetingRequest) (MeetingResult, error)

	// DeleteMeeting is a no-op for stateless providers.
	DeleteMeeting(ctx context.Context, externalID string) error

	Name() string
}

type MeetingRequest struct {
	BookingID   string
	Summary     string
	StartUTC    time.Time
	EndUTC      time.Time
	HostMail    string
	HostName    string
	InviteeMail string
	InviteeName string
}

type MeetingResult struct {
	JoinURL    string
	ExternalID string
}

// ----- Payment -----

type PaymentProvider interface {
	// CreateCheckout starts a payment session for a booking and returns
	// a redirect URL the invitee should be sent to plus a provider-side ID
	// the inbound webhook will reference.
	CreateCheckout(ctx context.Context, in CheckoutRequest) (Checkout, error)

	// VerifyWebhook validates the raw HTTP body + headers against the
	// provider's signature scheme. Returns the typed event so the caller
	// can dispatch into the booking pipeline. Idempotency is the caller's
	// concern; the verifier just authenticates the payload.
	VerifyWebhook(rawBody []byte, headers WebhookHeaders) (WebhookEvent, error)

	// Refund triggers a refund on a previously-captured payment.
	Refund(ctx context.Context, externalID string, amountCents int, currency string) error

	// Capture finalises a hold/authorisation. No-op when the provider's
	// flow already captures during checkout.
	Capture(ctx context.Context, externalID string, amountCents int) error

	Name() string
}

type CheckoutMode string

const (
	ModeFixed        CheckoutMode = "fixed"
	ModeDeposit      CheckoutMode = "deposit"
	ModeHold         CheckoutMode = "hold"
	ModeSubscription CheckoutMode = "subscription"
	ModeOpen         CheckoutMode = "open"
)

type CheckoutRequest struct {
	BookingID     string
	Mode          CheckoutMode
	AmountCents   int
	Currency      string
	Description   string
	InviteeMail   string
	InviteeName   string
	SuccessURL    string
	CancelURL     string
	StripePriceID string // for subscription mode (Stripe price id, or PayPal plan id)

	// v0.3 Stripe Connect: when non-empty, the Stripe adapter sets
	// "Stripe-Account: <id>" so the checkout runs on the host's connected
	// account. application_fee_amount is taken in cents; 0 means none.
	ConnectAccountID    string
	ApplicationFeeCents int
}

type Checkout struct {
	RedirectURL string
	ExternalID  string
}

// WebhookHeaders captures the subset of headers we need for signature
// verification without coupling to net/http.
type WebhookHeaders map[string]string

// Get is case-insensitive lookup.
func (h WebhookHeaders) Get(key string) string {
	if v, ok := h[key]; ok {
		return v
	}
	// fallback: linear scan with case-fold (cheap for our few headers)
	for k, v := range h {
		if equalFold(k, key) {
			return v
		}
	}
	return ""
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// WebhookEventType is the small set of payment outcomes we care about.
// Provider-specific event names map onto these in each adapter.
type WebhookEventType string

const (
	EventPaymentSucceeded WebhookEventType = "payment.succeeded"
	EventPaymentFailed    WebhookEventType = "payment.failed"
	EventPaymentExpired   WebhookEventType = "payment.expired"
	EventPaymentRefunded  WebhookEventType = "payment.refunded"
)

// WebhookEvent is what VerifyWebhook returns after authenticating the body.
type WebhookEvent struct {
	Type        WebhookEventType
	EventID     string // provider's event id; used for idempotency
	ExternalID  string // payment / checkout-session id ↔ bookings.payment_external_id
	BookingID   string // populated when we passed it as client_reference_id
	AmountCents int
	Currency    string
	OccurredAt  time.Time
	Raw         map[string]any // full decoded payload for debugging
}

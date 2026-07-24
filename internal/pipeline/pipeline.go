// Package pipeline orchestrates the booking-creation flow described in
// docs/planning/03 (validate → intake → price → reserve → pay → meeting →
// calendar → notify). Phase 2 implements validate/intake/price/reserve/notify;
// pay/meeting/calendar are no-op stubs that Phase 3 fills in.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/intake"
	"github.com/qognio/qognical/internal/notifier"
	"github.com/qognio/qognical/internal/slot"
	"github.com/qognio/qognical/internal/state"
	"github.com/qognio/qognical/internal/store"
	"github.com/qognio/qognical/internal/timeutil"
	"github.com/qognio/qognical/internal/token"
	"github.com/qognio/qognical/internal/webhooks"
)

// Request is what the API layer (or a Service-Token caller) submits.
type Request struct {
	EventTypeID     string
	StartUTC        time.Time
	InviteeName     string
	InviteeEmail    string
	InviteePhone    string
	InviteeTimezone string
	IntakeData      map[string]any
	Actor           string // "anonymous", "service:<name>", "host:<id>"
	IP              string
}

// Result is what's returned to the caller. Fee + CheckoutURL stay empty when
// the event-type is free; they will be populated by Phase 3 pay step.
type Result struct {
	Booking     store.Booking
	ManageToken token.Token
	CheckoutURL string
}

// Pipeline is the booking-creation orchestrator. Construct once; Run is safe
// for concurrent calls (each Run runs its own DB transaction in the reserve
// step).
type Pipeline struct {
	repo     *store.Repo
	tokens   *token.Service
	notifier Notifier
	now      func() time.Time
	baseURL  string

	// Phase 3 dependencies — all optional; nil means "skip that step".
	registry *adapters.Registry
	stripe   adapters.PaymentProvider
	paypal   adapters.PaymentProvider
	disp     *webhooks.Dispatcher
}

// Notifier is the subset of *notifier.Notifier we need; lets tests stub it.
type Notifier interface {
	SendConfirmation(notifier.Booking) error
	SendCancellation(notifier.Booking) error
	SendReschedule(notifier.Booking, int) error

	// v1.1 approval-workflow
	SendApprovalRequest(b notifier.Booking, declineURL string) error
	SendApprovalPending(notifier.Booking) error
	SendApprovalCheckout(notifier.Booking) error
	SendApprovalDeclined(b notifier.Booking, reason string) error
}

func New(repo *store.Repo, tokens *token.Service, n Notifier, baseURL string) *Pipeline {
	return &Pipeline{
		repo:     repo,
		tokens:   tokens,
		notifier: n,
		now:      func() time.Time { return time.Now().UTC() },
		baseURL:  baseURL,
	}
}

// WithRegistry plugs in Phase-3 adapters. Safe to call after construction;
// the pipeline still works with nil values (those steps become no-ops).
func (p *Pipeline) WithRegistry(r *adapters.Registry) *Pipeline { p.registry = r; return p }

// WithPayment wires payment providers, looked up by event_types.payment_provider.
func (p *Pipeline) WithPayment(stripe, paypal adapters.PaymentProvider) *Pipeline {
	p.stripe = stripe
	p.paypal = paypal
	return p
}

// WithDispatcher wires the outbound-webhook dispatcher.
func (p *Pipeline) WithDispatcher(d *webhooks.Dispatcher) *Pipeline { p.disp = d; return p }

// WithClock is a test seam — pass time.Now in production.
func (p *Pipeline) WithClock(clock func() time.Time) *Pipeline {
	p.now = clock
	return p
}

// Errors mirror the public-API error codes in Doc 06.
var (
	ErrEventTypeInactive       = errors.New("event_type_inactive")
	ErrSlotOutsideAvailability = errors.New("slot_outside_availability")
	ErrSlotUnavailable         = errors.New("slot_unavailable")
	ErrSlotTooSoon             = errors.New("slot_too_soon")
	ErrSlotTooFar              = errors.New("slot_too_far")
	ErrInvalidRequest          = errors.New("invalid_request")
	ErrIntakeValidation        = errors.New("intake_validation_failed")
	ErrCapacityFull            = errors.New("slot_capacity_full")
	// ErrExternalCalendarUnavailable is returned when the host has a calendar
	// integration but its busy state could not be read during authoritative
	// validation (fail-closed). Maps to 503 so the client retries.
	ErrExternalCalendarUnavailable = errors.New("external_calendar_unavailable")
)

// Run executes the full creation pipeline.
func (p *Pipeline) Run(req Request) (Result, error) {
	et, err := p.repo.FindEventTypeByID(req.EventTypeID)
	if err != nil {
		return Result{}, ErrInvalidRequest
	}

	// v1.1: select host from the pool (single-host returns owner immediately).
	endUTC := req.StartUTC.Add(time.Duration(et.DurationMin) * time.Minute)
	hostID, err := p.pickHost(et, req.StartUTC, endUTC)
	if err != nil {
		return Result{}, fmt.Errorf("host selection: %w", err)
	}
	host, err := p.repo.FindHostByID(hostID)
	if err != nil {
		return Result{}, ErrInvalidRequest
	}

	// 1. validate
	if err := p.validate(req, et, host, endUTC); err != nil {
		return Result{}, err
	}

	// v1.1 capacity check (group events): allow up to event_type.capacity
	// concurrent bookings on the same slot.
	if et.EffectiveCapacity() > 1 {
		count, err := p.repo.CountActiveAtSlot(et.ID, req.StartUTC, endUTC)
		if err == nil && count >= et.EffectiveCapacity() {
			return Result{}, ErrCapacityFull
		}
	}

	// 2. intake
	schema, err := intake.ParseSchema(et.IntakeSchema)
	if err != nil {
		return Result{}, fmt.Errorf("intake schema corrupt: %w", err)
	}
	if err := schema.Validate(req.IntakeData); err != nil {
		return Result{}, fmt.Errorf("%w: %s", ErrIntakeValidation, err.Error())
	}

	// 3. price (server-side per INV-3) + approval routing (v1.1)
	status := state.StatusConfirmed
	paymentStatus := "none"
	if et.RequiresApproval {
		// Approval comes first; payment + meeting + calendar happen
		// only after host approval.
		status = state.StatusPendingApproval
	} else if et.PaymentMode != "" && et.PaymentMode != "none" {
		status = state.StatusPendingPayment
		paymentStatus = "pending"
	}

	// 4. reserve. For group events we skip the overlap check (capacity
	// handles concurrent attendees) by passing the booking through with a
	// shared group_session_id.
	draft := store.BookingDraft{
		EventTypeID:         et.ID,
		HostID:              host.ID,
		StartUTC:            req.StartUTC,
		EndUTC:              endUTC,
		InviteeName:         req.InviteeName,
		InviteeEmail:        req.InviteeEmail,
		InviteePhone:        req.InviteePhone,
		InviteeTimezone:     req.InviteeTimezone,
		IntakeData:          req.IntakeData,
		IntakeSchemaVersion: et.SchemaVersion,
		Status:              status,
		PaymentStatus:       paymentStatus,
	}
	// For group events, give every attendee of the same slot the same
	// group_session_id (deterministic from (event_type, start_utc)). Capacity
	// is passed through so ReserveBookingTx can enforce it atomically — the
	// pre-check above is a fast fail, the in-tx check is authoritative.
	if et.EffectiveCapacity() > 1 {
		draft.GroupSessionID = fmt.Sprintf("grp_%s_%d", et.ID, req.StartUTC.Unix())
		draft.Capacity = et.EffectiveCapacity()
	}
	booking, err := p.repo.ReserveBookingTx(draft)
	if err != nil {
		if errors.Is(err, store.ErrSlotTaken) {
			return Result{}, ErrSlotUnavailable
		}
		if errors.Is(err, store.ErrCapacityFull) {
			return Result{}, ErrCapacityFull
		}
		return Result{}, fmt.Errorf("reserve: %w", err)
	}

	// 5. issue manage-token + store hash. Done before notify so the email
	//    includes the working link.
	tok, err := p.tokens.Issue(booking.ID, token.ActionView, 0)
	if err != nil {
		return Result{}, err
	}
	if err := p.repo.SetCancelTokenHash(booking.ID, tok.Hash); err != nil {
		return Result{}, err
	}

	// 6/7. Routing per status: approval-pending → host notify with
	// approve/decline-token. Paid → start checkout. Free → confirm tail.
	result := Result{Booking: booking, ManageToken: tok}
	switch status {
	case state.StatusPendingApproval:
		approvalTok, err := p.tokens.Issue(booking.ID, token.ActionView, 7*24*time.Hour)
		if err == nil {
			_ = p.repo.SetApprovalTokenHash(booking.ID, approvalTok.Hash)
		}
		if err := p.notifyApprovalRequest(booking, et, host, approvalTok); err != nil {
			slog.Warn("approval notify failed", "booking", booking.ID, "err", err)
		}
	case state.StatusPendingPayment:
		checkout, err := p.startCheckout(booking, et, tok)
		if err != nil {
			slog.Warn("checkout start failed", "booking", booking.ID, "err", err)
			return Result{}, err
		}
		result.CheckoutURL = checkout.RedirectURL
		_, _ = p.repo.SetBookingPaymentResult(booking.ID, state.StatusPendingPayment, "pending", checkout.ExternalID, 0)
	default: // confirmed
		// Async: confirmTail erstellt Meeting + versendet Bestätigungsmails
		// (SMTP), was real 12–25s+ dauern kann. Die Buchung ist hier bereits
		// reserviert (Schritt 4) + der Manage-Token ausgestellt (Schritt 5);
		// die HTTP-Antwort (status/manage_url, api.go) hängt NICHT von
		// confirmTail ab. Synchron blockierte der Handler bis zum Mailversand
		// → Client-Timeout beim Bridge-POST TROTZ erfolgreicher Buchung
		// (verifiziert 2026-07-24). confirmTail hat ein eigenes 30s-Context-
		// Timeout + loggt Fehler; Meeting/Mail sind best-effort (INV-5).
		go func(b store.Booking, et store.EventType, h store.Host, t token.Token) {
			// net/http only recovers panics in the request goroutine, not in
			// spawned ones — without this a panic in the meeting/SMTP path
			// (adapter, template, notifier) would crash the whole process and
			// take down every booking, not just this one.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("confirm tail panic", "booking", b.ID, "panic", r)
				}
			}()
			if err := p.confirmTail(b, et, h, t); err != nil {
				slog.Warn("confirm tail failed", "booking", b.ID, "err", err)
			}
		}(booking, et, host, tok)
	}

	_ = p.repo.WriteAudit(store.AuditEntry{
		Actor:      req.Actor,
		Action:     "booking.created",
		TargetType: "booking",
		TargetID:   booking.ID,
		IP:         req.IP,
		Metadata: map[string]any{
			"event_type": et.Slug,
			"start_utc":  req.StartUTC.Format(time.RFC3339),
			"status":     string(status),
		},
	})

	p.emit(host.ID, webhooks.EventBookingCreated, p.webhookData(booking, et, host))
	if status == state.StatusConfirmed {
		p.emit(host.ID, webhooks.EventBookingConfirmed, p.webhookData(booking, et, host))
	}

	return result, nil
}

// startCheckout selects the payment provider and runs CreateCheckout.
func (p *Pipeline) startCheckout(b store.Booking, et store.EventType, tok token.Token) (adapters.Checkout, error) {
	prov := p.paymentFor(et.PaymentProvider)
	if prov == nil {
		return adapters.Checkout{}, fmt.Errorf("payment provider %q not configured", et.PaymentProvider)
	}
	mode := mapMode(et.PaymentMode)
	if mode == "" {
		return adapters.Checkout{}, fmt.Errorf("unsupported payment_mode %q", et.PaymentMode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return prov.CreateCheckout(ctx, adapters.CheckoutRequest{
		BookingID:     b.ID,
		Mode:          mode,
		AmountCents:   et.PaymentAmount,
		Currency:      et.PaymentCurrency,
		Description:   et.Title,
		InviteeMail:   b.InviteeEmail,
		InviteeName:   b.InviteeName,
		SuccessURL:    fmt.Sprintf("%s/manage/%s?token=%s", p.baseURL, b.ID, tok.String),
		CancelURL:     fmt.Sprintf("%s/book/%s/%s", p.baseURL, et.OwnerID, et.Slug),
		StripePriceID: et.StripePriceID,
	})
}

func (p *Pipeline) paymentFor(name string) adapters.PaymentProvider {
	switch name {
	case "stripe":
		return p.stripe
	case "paypal":
		return p.paypal
	}
	return nil
}

func mapMode(m string) adapters.CheckoutMode {
	switch m {
	case "fixed":
		return adapters.ModeFixed
	case "deposit":
		return adapters.ModeDeposit
	case "hold":
		return adapters.ModeHold
	case "subscription":
		return adapters.ModeSubscription
	case "open":
		return adapters.ModeOpen
	}
	return ""
}

// confirmTail runs after a booking enters "confirmed" — either directly (free
// event-type) or via the payment webhook. Creates meeting + external calendar
// entry per INV-5 ("only at confirmed"), then notifies.
func (p *Pipeline) confirmTail(b store.Booking, et store.EventType, host store.Host, tok token.Token) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Demo / webinar mode: if the event-type carries a fixed join URL
	// (meeting_config.fixed_join_url), mail THAT link to every attendee and
	// skip meeting + calendar creation entirely. Auto-created Teams meetings
	// put external attendees in a lobby they can't leave (2026-07-23 NEXUS LIVE
	// incident) — a fixed link the host set to "everyone bypasses the lobby"
	// lets anyone just click and join. No per-booking Graph event, no lobby,
	// one shared room for the whole session.
	if fixed := fixedJoinURL(et.MeetingConfig); fixed != "" {
		// Empty provider: external_calendar_provider is a SelectField that only
		// accepts the real calendar providers, so "fixed" would fail validation
		// and abort the whole Save (meeting_join_url included). We only need the
		// join URL here — no external calendar event exists.
		if err := p.repo.PersistBookingExternals(b.ID, "", "", fixed); err != nil {
			slog.Error("fixed-link persist failed", "booking", b.ID, "err", err)
		}
		b.MeetingJoinURL = fixed
		return p.notifyConfirmed(b, et, host, tok)
	}

	// Calendar / Meeting in one shot when the calendar adapter natively
	// hosts the meeting (MS Graph → Teams Weg 1, Google → Google Meet).
	// The load error must NOT be discarded: a decrypt failure (e.g. the
	// plaintext-credentials bug fixed 2026-07-16) otherwise degrades every
	// booking to "confirmed without meeting" with zero trace. The booking
	// still confirms (INV-5: meeting creation is best-effort), but loudly.
	calProv, calErr := p.registryFor(host.ID)
	if calErr != nil {
		slog.Error("calendar integration load failed — booking will confirm WITHOUT meeting",
			"booking", b.ID, "host", host.ID, "err", calErr)
		p.recordIntegrationError(host.ID, "load: "+calErr.Error())
	}
	inlineMeeting := false
	if calProv != nil {
		switch {
		case et.LocationType == "online_teams" && (calProv.Name() == "msgraph" || calProv.Name() == "microsoft"):
			inlineMeeting = true
		case et.LocationType == "online_google_meet" && calProv.Name() == "google":
			inlineMeeting = true
		}
	}
	cev := adapters.CalendarEvent{
		Summary:             fmt.Sprintf("%s mit %s", et.Title, b.InviteeName),
		Description:         et.Description,
		StartUTC:            b.StartUTC,
		EndUTC:              b.EndUTC,
		OrganizerMail:       host.Email,
		AttendeeName:        b.InviteeName,
		AttendeeMail:        b.InviteeEmail,
		CreateOnlineMeeting: inlineMeeting,
	}
	var meetingURL, externalID string
	// Group event (webinar): reuse the ONE meeting created by the first
	// attendee of this slot so everyone joins the same room, instead of each
	// booking spawning its own isolated Teams meeting (2026-07-23). Only the
	// first booking of the group_session falls through to CreateEvent.
	if b.GroupSessionID != "" {
		if url, extID, ok := p.repo.FindGroupMeeting(b.GroupSessionID, b.ID); ok {
			meetingURL, externalID = url, extID
		}
	}
	if meetingURL == "" && calProv != nil {
		created, err := calProv.CreateEvent(ctx, cev)
		if err != nil {
			slog.Error("calendar create failed — booking confirms WITHOUT meeting",
				"booking", b.ID, "host", host.ID, "provider", calProv.Name(), "err", err)
			p.recordIntegrationError(host.ID, calProv.Name()+": "+err.Error())
		} else {
			externalID = created.ExternalID
			meetingURL = created.MeetingURL
			p.recordIntegrationError(host.ID, "") // clear stale last_error
		}
	}
	// Standalone meeting providers (calendar adapter didn't supply a URL).
	// Jitsi is instance-scoped (config on the event-type); Zoom is per-host
	// integration. v0.3.
	if meetingURL == "" && p.registry != nil {
		var meet adapters.MeetingProvider
		switch et.LocationType {
		case "online_jitsi":
			meet, _ = p.registry.MeetingForName("jitsi", []byte(et.MeetingConfig))
		case "online_zoom":
			meet, _ = p.registry.MeetingForHost(host.ID, "zoom")
		}
		if meet != nil {
			res, err := meet.CreateMeeting(ctx, adapters.MeetingRequest{
				BookingID: b.ID, Summary: et.Title,
				StartUTC: b.StartUTC, EndUTC: b.EndUTC,
				HostMail: host.Email, HostName: host.Name,
				InviteeMail: b.InviteeEmail, InviteeName: b.InviteeName,
			})
			if err == nil {
				meetingURL = res.JoinURL
			} else {
				slog.Warn("meeting create failed", "provider", et.LocationType, "booking", b.ID, "err", err)
			}
		}
	}
	if meetingURL != "" || externalID != "" {
		_ = p.repo.PersistBookingExternals(b.ID, externalID, calendarProviderName(calProv), meetingURL)
		b.ExternalCalendarID = externalID
		b.MeetingJoinURL = meetingURL
	}
	return p.notifyConfirmed(b, et, host, tok)
}

// fixedJoinURL reads meeting_config.fixed_join_url from an event-type's
// meeting_config JSON. Empty when unset/malformed → the normal auto-create
// flow runs. Only https Teams/meet URLs are accepted so a bad config can't
// mail a junk or non-https link.
func fixedJoinURL(meetingConfig types.JSONRaw) string {
	if len(meetingConfig) == 0 {
		return ""
	}
	var cfg struct {
		FixedJoinURL string `json:"fixed_join_url"`
	}
	if err := json.Unmarshal(meetingConfig, &cfg); err != nil {
		return ""
	}
	u := strings.TrimSpace(cfg.FixedJoinURL)
	if strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}

// recordIntegrationError persists msg to last_error on the host's calendar
// integration row(s) so failures surface in the host UI/CLI, not only in
// logs. Empty msg clears a stale error. Best-effort by design.
func (p *Pipeline) recordIntegrationError(hostID, msg string) {
	if err := p.repo.SetCalendarIntegrationError(hostID, msg); err != nil {
		slog.Warn("could not persist integration last_error", "host", hostID, "err", err)
	}
}

// registryFor returns (calendar provider for host, ok). Returns nil if no
// integration is configured — that's fine, booking stays local-only.
func (p *Pipeline) registryFor(hostID string) (adapters.CalendarProvider, error) {
	if p.registry == nil {
		return nil, nil
	}
	return p.registry.CalendarForHost(hostID)
}

// ExternalBusy returns the host's busy intervals from their connected calendar
// (CalendarProvider.FreeBusy) between from and to, or (nil, nil) when no
// calendar integration is configured. It manages its own bounded context
// (the shared HTTP client also enforces a per-call timeout).
//
// Callers choose the failure policy: the authoritative booking validation
// fails CLOSED (refuse when busy state is unknown), while slot display fails
// OPEN (show the grid, let validation catch conflicts at booking time).
func (p *Pipeline) ExternalBusy(hostID string, from, to time.Time) ([]timeutil.Interval, error) {
	prov, err := p.registryFor(hostID)
	if err != nil {
		return nil, err
	}
	if prov == nil {
		return nil, nil // no calendar integration → no external constraint
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return prov.FreeBusy(ctx, from, to)
}

func calendarProviderName(p adapters.CalendarProvider) string {
	if p == nil {
		return ""
	}
	return p.Name()
}

// OnPaymentSucceeded is the hook the inbound webhook handler calls when
// Stripe/PayPal confirms a payment. It promotes the booking, runs the
// confirm-tail, and emits booking.confirmed.
func (p *Pipeline) OnPaymentSucceeded(bookingID string) error {
	b, err := p.repo.FindBookingByID(bookingID)
	if err != nil {
		return err
	}
	et, err := p.repo.FindEventTypeByID(b.EventTypeID)
	if err != nil {
		return err
	}
	host, err := p.repo.FindHostByID(b.HostID)
	if err != nil {
		return err
	}
	tok, _ := p.tokens.Issue(b.ID, token.ActionView, 0)
	_ = p.repo.SetCancelTokenHash(b.ID, tok.Hash)
	if err := p.confirmTail(b, et, host, tok); err != nil {
		slog.Warn("payment-confirm tail failed", "booking", b.ID, "err", err)
	}
	p.emit(host.ID, webhooks.EventBookingConfirmed, p.webhookData(b, et, host))
	return nil
}

func (p *Pipeline) emit(hostID, event string, data map[string]any) {
	if p.disp == nil {
		return
	}
	_ = p.disp.Emit(hostID, event, data)
}

func (p *Pipeline) webhookData(b store.Booking, et store.EventType, host store.Host) map[string]any {
	return map[string]any{
		"booking_id": b.ID,
		"event_type": map[string]any{"id": et.ID, "title": et.Title, "slug": et.Slug},
		"host":       map[string]any{"id": host.ID, "name": host.Name},
		"invitee": map[string]any{
			"name": b.InviteeName, "email": b.InviteeEmail, "phone": b.InviteePhone,
		},
		"start_utc":   b.StartUTC.Format(time.RFC3339),
		"end_utc":     b.EndUTC.Format(time.RFC3339),
		"meeting_url": b.MeetingJoinURL,
		"intake_data": b.IntakeData,
	}
}

func (p *Pipeline) validate(req Request, et store.EventType, host store.Host, endUTC time.Time) error {
	if !et.Active {
		return ErrEventTypeInactive
	}
	if req.InviteeName == "" || req.InviteeEmail == "" || req.InviteeTimezone == "" {
		return ErrInvalidRequest
	}
	now := p.now()
	if req.StartUTC.Before(now.Add(time.Duration(et.MinNoticeMin) * time.Minute)) {
		return ErrSlotTooSoon
	}
	if et.MaxHorizonDays > 0 && req.StartUTC.After(now.Add(time.Duration(et.MaxHorizonDays)*24*time.Hour)) {
		return ErrSlotTooFar
	}
	// Re-derive availability windows for the day and reject slots outside.
	weekRules, err := p.repo.FindAvailability(host.ID)
	if err != nil {
		return err
	}
	overrides, err := p.repo.FindOverridesInRange(host.ID, req.StartUTC.Add(-24*time.Hour), req.StartUTC.Add(48*time.Hour))
	if err != nil {
		return err
	}
	// Authoritative external-calendar check, FAIL CLOSED: if the host has a
	// calendar integration but we cannot read its busy windows, refuse the
	// booking rather than risk double-booking around an unknown external
	// calendar. (No integration → ExternalBusy returns nil and this is a no-op.)
	//
	// Only for SIMPLE single-host, single-attendee events. Group events
	// (capacity>1) deliberately allow several bookings on one slot — our own
	// group calendar event would otherwise block the 2nd attendee — and pooled
	// events need per-host checking that the owner alone can't provide; both
	// fall back to the local checks until per-host/pool-aware external checking
	// lands. The busy window is widened by the event's buffers so an external
	// event ending just inside the buffer still surfaces as a conflict.
	var externalBusy []timeutil.Interval
	if len(et.AllHosts()) == 1 && et.EffectiveCapacity() <= 1 {
		bufBefore := time.Duration(et.BufferBeforeMin) * time.Minute
		bufAfter := time.Duration(et.BufferAfterMin) * time.Minute
		eb, ebErr := p.ExternalBusy(host.ID,
			req.StartUTC.Add(-time.Minute-bufBefore), endUTC.Add(time.Minute+bufAfter))
		if ebErr != nil {
			return fmt.Errorf("%w: %v", ErrExternalCalendarUnavailable, ebErr)
		}
		externalBusy = eb
	}
	// Use slot.ComputeSlots with the busy interval set to "this exact slot,
	// shifted by 1 second" so the engine confirms the slot start lines up
	// with a windowed candidate. Cheap and reuses the production code path.
	slots, err := slot.ComputeSlots(slot.Input{
		EventType: slot.EventType{
			DurationMin:     et.DurationMin,
			BufferBeforeMin: et.BufferBeforeMin,
			BufferAfterMin:  et.BufferAfterMin,
			MinNoticeMin:    et.MinNoticeMin,
			MaxHorizonDays:  et.MaxHorizonDays,
		},
		HostTimezone: host.Timezone,
		Availability: weekRules,
		Overrides:    overrides,
		ExternalBusy: externalBusy,
		Now:          now,
		From:         req.StartUTC.Add(-time.Minute),
		To:           endUTC.Add(time.Minute),
	})
	if err != nil {
		return err
	}
	for _, s := range slots {
		if s.StartUTC.Equal(req.StartUTC) {
			return nil
		}
	}
	return ErrSlotOutsideAvailability
}

func (p *Pipeline) notifyConfirmed(b store.Booking, et store.EventType, host store.Host, tok token.Token) error {
	return p.notifier.SendConfirmation(notifier.Booking{
		ID:             b.ID,
		EventTypeTitle: et.Title,
		HostName:       host.Name,
		HostEmail:      host.Email,
		InviteeName:    b.InviteeName,
		InviteeEmail:   b.InviteeEmail,
		StartUTC:       b.StartUTC,
		EndUTC:         b.EndUTC,
		InviteeTZ:      b.InviteeTimezone,
		ManageURL:      manageURL(p.baseURL, b.ID, tok.String),
		MeetingURL:     b.MeetingJoinURL,
		BaseURL:        p.baseURL,
	})
}

// Cancel transitions confirmed → cancelled and rotates the token hash so
// the link can't be replayed (INV-8 / TOK-4).
func (p *Pipeline) Cancel(bookingID, reason, actor, ip string) (store.Booking, error) {
	b, err := p.repo.UpdateStatus(bookingID, state.StatusCancelled, map[string]any{
		"cancellation_reason": reason,
		"cancelled_at":        p.now(),
	})
	if err != nil {
		return store.Booking{}, err
	}
	// Rotate token — make any further token-based call fail.
	tok, err := p.tokens.Issue(bookingID, token.ActionView, time.Hour)
	if err == nil {
		_ = p.repo.SetCancelTokenHash(bookingID, tok.Hash)
	}
	et, _ := p.repo.FindEventTypeByID(b.EventTypeID)
	host, _ := p.repo.FindHostByID(b.HostID)

	// Cascade to external calendar / meeting providers.
	if b.ExternalCalendarID != "" {
		if calProv, _ := p.registryFor(host.ID); calProv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = calProv.DeleteEvent(ctx, b.ExternalCalendarID)
			cancel()
		}
	}

	_ = p.notifier.SendCancellation(notifier.Booking{
		ID:             b.ID,
		EventTypeTitle: et.Title,
		HostName:       host.Name,
		HostEmail:      host.Email,
		InviteeName:    b.InviteeName,
		InviteeEmail:   b.InviteeEmail,
		StartUTC:       b.StartUTC,
		EndUTC:         b.EndUTC,
		InviteeTZ:      b.InviteeTimezone,
		BaseURL:        p.baseURL,
	})
	_ = p.repo.WriteAudit(store.AuditEntry{
		Actor: actor, Action: "booking.cancelled",
		TargetType: "booking", TargetID: b.ID, IP: ip,
		Metadata: map[string]any{"reason": reason},
	})
	p.emit(host.ID, webhooks.EventBookingCancelled, p.webhookData(b, et, host))
	return b, nil
}

// Reschedule rewrites start/end of an existing booking and rotates the token.
// Per Doc 04 INV-1 + INV-8 the recheck inside ReplaceStartEnd is required.
func (p *Pipeline) Reschedule(bookingID string, newStart time.Time, actor, ip string) (store.Booking, token.Token, error) {
	current, err := p.repo.FindBookingByID(bookingID)
	if err != nil {
		return store.Booking{}, token.Token{}, err
	}
	et, err := p.repo.FindEventTypeByID(current.EventTypeID)
	if err != nil {
		return store.Booking{}, token.Token{}, err
	}
	host, err := p.repo.FindHostByID(current.HostID)
	if err != nil {
		return store.Booking{}, token.Token{}, err
	}

	// Validate new start against availability — re-use the same code path.
	newEnd := newStart.Add(time.Duration(et.DurationMin) * time.Minute)
	if err := p.validate(Request{
		EventTypeID: et.ID, StartUTC: newStart,
		InviteeName: current.InviteeName, InviteeEmail: current.InviteeEmail, InviteeTimezone: current.InviteeTimezone,
	}, et, host, newEnd); err != nil {
		return store.Booking{}, token.Token{}, err
	}

	updated, err := p.repo.ReplaceStartEnd(bookingID, newStart, newEnd)
	if err != nil {
		if errors.Is(err, store.ErrSlotTaken) {
			return store.Booking{}, token.Token{}, ErrSlotUnavailable
		}
		return store.Booking{}, token.Token{}, err
	}

	tok, err := p.tokens.Issue(bookingID, token.ActionView, 0)
	if err == nil {
		_ = p.repo.SetCancelTokenHash(bookingID, tok.Hash)
	}
	_ = p.notifier.SendReschedule(notifier.Booking{
		ID:             updated.ID,
		EventTypeTitle: et.Title,
		HostName:       host.Name,
		HostEmail:      host.Email,
		InviteeName:    updated.InviteeName,
		InviteeEmail:   updated.InviteeEmail,
		StartUTC:       updated.StartUTC,
		EndUTC:         updated.EndUTC,
		InviteeTZ:      updated.InviteeTimezone,
		ManageURL:      manageURL(p.baseURL, updated.ID, tok.String),
		BaseURL:        p.baseURL,
	}, 1)
	_ = p.repo.WriteAudit(store.AuditEntry{
		Actor: actor, Action: "booking.rescheduled",
		TargetType: "booking", TargetID: updated.ID, IP: ip,
		Metadata: map[string]any{"new_start_utc": newStart.Format(time.RFC3339)},
	})

	// Update external calendar entry, if any.
	if current.ExternalCalendarID != "" {
		if calProv, _ := p.registryFor(host.ID); calProv != nil {
			ctxBg, cancelBg := context.WithTimeout(context.Background(), 10*time.Second)
			_ = calProv.UpdateEvent(ctxBg, current.ExternalCalendarID, adapters.CalendarEvent{
				Summary:  et.Title + " mit " + updated.InviteeName,
				StartUTC: updated.StartUTC, EndUTC: updated.EndUTC,
				OrganizerMail: host.Email, AttendeeMail: updated.InviteeEmail, AttendeeName: updated.InviteeName,
			})
			cancelBg()
		}
	}
	p.emit(host.ID, webhooks.EventBookingRescheduled, p.webhookData(updated, et, host))
	return updated, tok, nil
}

func manageURL(base, bookingID, tok string) string {
	return fmt.Sprintf("%s/manage/%s?token=%s", base, bookingID, tok)
}

// ManageURL is the exported version used by the api package to render
// invitee-facing links with the same shape as the email templates.
func (p *Pipeline) ManageURL(bookingID, tok string) string {
	return manageURL(p.baseURL, bookingID, tok)
}

// quiet the unused import lint when slot is only referenced via constants.
var _ = timeutil.Interval{}

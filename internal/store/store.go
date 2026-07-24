// Package store wraps PocketBase record access for the booking domain. Code
// outside this package should never touch *core.Record directly — the
// abstraction stays thin (no ORM, no caching) but it does isolate field
// names and conversions in one place. That makes future migrations less
// painful and keeps tests focused on the booking pipeline rather than PB.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"

	"github.com/qognio/qognical/internal/slot"
	"github.com/qognio/qognical/internal/state"
	"github.com/qognio/qognical/internal/timeutil"
	"github.com/qognio/qognical/migrations"
)

// Repo holds the app handle and exposes the queries the booking pipeline needs.
// It does not own a transaction; pass txApp into the constructor when
// operating inside core.App.RunInTransaction.
type Repo struct {
	app core.App
}

func New(app core.App) *Repo { return &Repo{app: app} }

// WithTx returns a Repo bound to the transactional app. Use inside
// app.RunInTransaction to keep reads + writes in the same atomic step.
func (r *Repo) WithTx(txApp core.App) *Repo { return &Repo{app: txApp} }

// ----- users (hosts) -----

type Host struct {
	ID    string
	Email string
	Name  string
	// Slug is the host's public URL segment (/book/{Slug}/{event}). Optional:
	// hosts without one are still addressable by email, so links minted before
	// slugs existed keep working.
	Slug     string
	Timezone string
	Role     string
}

// mustDateTime wraps the (DateTime, error) tuple — for booking records we
// always pass time.Time values, which ParseDateTime accepts without error.
func mustDateTime(v any) types.DateTime {
	d, _ := types.ParseDateTime(v)
	return d
}

func recordToHost(r *core.Record) Host {
	return Host{
		ID:       r.Id,
		Email:    r.Email(),
		Name:     r.GetString("name"),
		Slug:     r.GetString("slug"),
		Timezone: r.GetString("timezone"),
		Role:     r.GetString("role"),
	}
}

func (r *Repo) FindHostByID(id string) (Host, error) {
	rec, err := r.app.FindRecordById(migrations.CollUsers, id)
	if err != nil {
		return Host{}, err
	}
	return recordToHost(rec), nil
}

// FindHostByEmail looks up a host (any role) by their PocketBase auth email.
func (r *Repo) FindHostByEmail(email string) (Host, error) {
	rec, err := r.app.FindAuthRecordByEmail(migrations.CollUsers, email)
	if err != nil {
		return Host{}, err
	}
	return recordToHost(rec), nil
}

// FindHostBySlug looks up a host by their public URL slug. An empty slug must
// never match — many hosts legitimately have none, and matching "" would hand
// out an arbitrary one of them.
func (r *Repo) FindHostBySlug(slug string) (Host, error) {
	if slug == "" {
		return Host{}, errors.New("empty host slug")
	}
	rec, err := r.app.FindFirstRecordByData(migrations.CollUsers, "slug", slug)
	if err != nil {
		return Host{}, err
	}
	return recordToHost(rec), nil
}

// ----- event_types -----

type EventType struct {
	ID              string
	OwnerID         string
	Slug            string
	Title           string
	Description     string
	DurationMin     int
	BufferBeforeMin int
	BufferAfterMin  int
	MinNoticeMin    int
	MaxHorizonDays  int
	LocationType    string
	MeetingConfig   types.JSONRaw
	IntakeSchema    types.JSONRaw
	PaymentMode     string
	PaymentAmount   int
	PaymentCurrency string
	PaymentProvider string
	StripePriceID   string
	SchemaVersion   int
	Active          bool

	// v1.1 additions
	Hosts              []string
	AssignmentStrategy string
	Capacity           int
	RequiresApproval   bool
	BrandColor         string
	BrandLogoURL       string
	CustomCSS          types.JSONRaw
	Locale             string
}

// AllHosts returns the union of owner + hosts[] as the canonical list of
// people who could take this booking. v1.0 callers see just owner.
func (e EventType) AllHosts() []string {
	if len(e.Hosts) == 0 {
		return []string{e.OwnerID}
	}
	seen := map[string]bool{e.OwnerID: true}
	out := []string{e.OwnerID}
	for _, h := range e.Hosts {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// EffectiveCapacity returns max(Capacity, 1) — DB default is 0 but
// semantically 1 (single-attendee event).
func (e EventType) EffectiveCapacity() int {
	if e.Capacity <= 0 {
		return 1
	}
	return e.Capacity
}

func recordToEventType(r *core.Record) EventType {
	return EventType{
		ID:                 r.Id,
		OwnerID:            r.GetString("owner"),
		Slug:               r.GetString("slug"),
		Title:              r.GetString("title"),
		Description:        r.GetString("description"),
		DurationMin:        r.GetInt("duration_min"),
		BufferBeforeMin:    r.GetInt("buffer_before_min"),
		BufferAfterMin:     r.GetInt("buffer_after_min"),
		MinNoticeMin:       r.GetInt("min_notice_min"),
		MaxHorizonDays:     r.GetInt("max_horizon_days"),
		LocationType:       r.GetString("location_type"),
		MeetingConfig:      getJSON(r, "meeting_config"),
		IntakeSchema:       getJSON(r, "intake_schema"),
		PaymentMode:        r.GetString("payment_mode"),
		PaymentAmount:      r.GetInt("payment_amount"),
		PaymentCurrency:    r.GetString("payment_currency"),
		PaymentProvider:    r.GetString("payment_provider"),
		StripePriceID:      r.GetString("stripe_price_id"),
		SchemaVersion:      r.GetInt("schema_version"),
		Active:             r.GetBool("active"),
		Hosts:              r.GetStringSlice("hosts"),
		AssignmentStrategy: r.GetString("assignment_strategy"),
		Capacity:           r.GetInt("capacity"),
		RequiresApproval:   r.GetBool("requires_approval"),
		BrandColor:         r.GetString("brand_color"),
		BrandLogoURL:       r.GetString("brand_logo_url"),
		CustomCSS:          getJSON(r, "custom_css"),
		Locale:             r.GetString("locale"),
	}
}

func getJSON(r *core.Record, key string) types.JSONRaw {
	v := r.Get(key)
	if v == nil {
		return nil
	}
	if jr, ok := v.(types.JSONRaw); ok {
		return jr
	}
	return nil
}

func (r *Repo) FindEventType(hostID, slug string) (EventType, error) {
	rec, err := r.app.FindFirstRecordByFilter(
		migrations.CollEventTypes,
		"owner = {:owner} && slug = {:slug}",
		dbx.Params{"owner": hostID, "slug": slug},
	)
	if err != nil {
		return EventType{}, err
	}
	return recordToEventType(rec), nil
}

// ListActiveEventTypes returns every event_type with active=true. Used by
// the root landing page directory. The repo doesn't filter by host
// visibility — that's the caller's job (today: everything is shown).
func (r *Repo) ListActiveEventTypes() ([]EventType, error) {
	recs, err := r.app.FindAllRecords(migrations.CollEventTypes,
		dbx.NewExp("active = true"),
	)
	if err != nil {
		return nil, err
	}
	out := make([]EventType, 0, len(recs))
	for _, rec := range recs {
		out = append(out, recordToEventType(rec))
	}
	return out, nil
}

func (r *Repo) FindEventTypeByID(id string) (EventType, error) {
	rec, err := r.app.FindRecordById(migrations.CollEventTypes, id)
	if err != nil {
		return EventType{}, err
	}
	return recordToEventType(rec), nil
}

// ----- availability + overrides -----

func (r *Repo) FindAvailability(ownerID string) ([]slot.WeekRule, error) {
	recs, err := r.app.FindAllRecords(migrations.CollAvailability,
		dbx.HashExp{"owner": ownerID})
	if err != nil {
		return nil, err
	}
	out := make([]slot.WeekRule, 0, len(recs))
	for _, rec := range recs {
		out = append(out, slot.WeekRule{
			Weekday: rec.GetInt("weekday"),
			Start:   rec.GetString("start"),
			End:     rec.GetString("end"),
		})
	}
	return out, nil
}

func (r *Repo) FindOverridesInRange(ownerID string, from, to time.Time) ([]slot.DateOverride, error) {
	recs, err := r.app.FindAllRecords(migrations.CollDateOverrides,
		dbx.HashExp{"owner": ownerID},
		dbx.NewExp("date >= {:from} AND date < {:to}", dbx.Params{"from": from, "to": to}),
	)
	if err != nil {
		return nil, err
	}
	out := make([]slot.DateOverride, 0, len(recs))
	for _, rec := range recs {
		t := slot.OverrideUnavailable
		if rec.GetString("type") == "custom_hours" {
			t = slot.OverrideCustomHours
		}
		out = append(out, slot.DateOverride{
			Date:  rec.GetDateTime("date").Time(),
			Type:  t,
			Start: rec.GetString("start"),
			End:   rec.GetString("end"),
		})
	}
	return out, nil
}

// ----- bookings -----

type Booking struct {
	ID                  string
	EventTypeID         string
	HostID              string
	StartUTC            time.Time
	EndUTC              time.Time
	InviteeName         string
	InviteeEmail        string
	InviteePhone        string
	InviteeTimezone     string
	Status              state.Status
	IntakeData          map[string]any
	IntakeSchemaVersion int
	PaymentStatus       string
	PaymentExternalID   string
	PaymentAmountPaid   int
	MeetingJoinURL      string
	ExternalCalendarID  string
	CancelTokenHash     string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CancelledAt         time.Time
	CancellationReason  string

	// v1.1 additions
	ApprovalTokenHash string
	ApprovedAt        time.Time
	ApprovedBy        string
	DeclinedAt        time.Time
	DeclinedReason    string
	GroupSessionID    string
}

func recordToBooking(r *core.Record) Booking {
	b := Booking{
		ID:                  r.Id,
		EventTypeID:         r.GetString("event_type"),
		HostID:              r.GetString("host"),
		StartUTC:            r.GetDateTime("start_utc").Time(),
		EndUTC:              r.GetDateTime("end_utc").Time(),
		InviteeName:         r.GetString("invitee_name"),
		InviteeEmail:        r.GetString("invitee_email"),
		InviteePhone:        r.GetString("invitee_phone"),
		InviteeTimezone:     r.GetString("invitee_timezone"),
		Status:              state.Status(r.GetString("status")),
		IntakeSchemaVersion: r.GetInt("intake_schema_version"),
		PaymentStatus:       r.GetString("payment_status"),
		PaymentExternalID:   r.GetString("payment_external_id"),
		PaymentAmountPaid:   r.GetInt("payment_amount_paid"),
		MeetingJoinURL:      r.GetString("meeting_join_url"),
		ExternalCalendarID:  r.GetString("external_calendar_id"),
		CancelTokenHash:     r.GetString("cancel_token_hash"),
		CreatedAt:           r.GetDateTime("created").Time(),
		UpdatedAt:           r.GetDateTime("updated").Time(),
		CancelledAt:         r.GetDateTime("cancelled_at").Time(),
		CancellationReason:  r.GetString("cancellation_reason"),
		ApprovalTokenHash:   r.GetString("approval_token_hash"),
		ApprovedAt:          r.GetDateTime("approved_at").Time(),
		ApprovedBy:          r.GetString("approved_by"),
		DeclinedAt:          r.GetDateTime("declined_at").Time(),
		DeclinedReason:      r.GetString("declined_reason"),
		GroupSessionID:      r.GetString("group_session_id"),
	}
	if raw := r.Get("intake_data"); raw != nil {
		if jr, ok := raw.(types.JSONRaw); ok && len(jr) > 0 {
			_ = jr.Scan(&b.IntakeData)
		}
		if m, ok := raw.(map[string]any); ok {
			b.IntakeData = m
		}
	}
	return b
}

// ----- v1.1 helpers -----

// CountActiveAtSlot returns how many active bookings already sit on the
// given (event_type, start_utc, end_utc) window. Used by the capacity check
// for group-events (event_types.capacity > 1).
//
// dbx.HashExp doesn't reliably match DateTime equality, so we pass the
// timestamp as an explicit binding.
func (r *Repo) CountActiveAtSlot(eventTypeID string, start, end time.Time) (int, error) {
	// COUNT(*), not FindAllRecords: a group-slot listing calls this once per
	// candidate slot, so materialising every row would balloon into thousands
	// of loaded records per anonymous request (2026-07-20).
	var n int
	err := r.app.DB().
		Select("COUNT(*)").
		From(migrations.CollBookings).
		Where(dbx.HashExp{"event_type": eventTypeID, "start_utc": mustDateTime(start)}).
		AndWhere(dbx.NewExp("status IN (" + state.ActiveStatusSQLList() + ")")).
		Row(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ActiveBusyForHostExcludingEventType is ActiveBusyForHost minus the bookings
// of one event-type. Group-slot listing uses it so the event's OWN attendees
// don't hide the slot (capacity gates those) while conflicts from the host's
// OTHER event-types still block it (2026-07-20).
func (r *Repo) ActiveBusyForHostExcludingEventType(hostID, excludeEventTypeID string, from, to time.Time) ([]timeutil.Interval, error) {
	recs, err := r.app.FindAllRecords(migrations.CollBookings,
		dbx.HashExp{"host": hostID},
		dbx.NewExp("event_type != {:et}", dbx.Params{"et": excludeEventTypeID}),
		dbx.NewExp("status IN ("+state.ActiveStatusSQLList()+")"),
		dbx.NewExp("end_utc > {:from} AND start_utc < {:to}", dbx.Params{"from": from, "to": to}),
	)
	if err != nil {
		return nil, err
	}
	out := make([]timeutil.Interval, 0, len(recs))
	for _, rec := range recs {
		out = append(out, timeutil.Interval{
			Start: rec.GetDateTime("start_utc").Time(),
			End:   rec.GetDateTime("end_utc").Time(),
		})
	}
	return out, nil
}

// HostLoadInWindow returns a map of host_id → count of active bookings in the
// window. Used by round_robin / least_loaded host selection.
func (r *Repo) HostLoadInWindow(hostIDs []string, from, to time.Time) (map[string]int, error) {
	out := make(map[string]int, len(hostIDs))
	for _, h := range hostIDs {
		out[h] = 0
		recs, err := r.app.FindAllRecords(migrations.CollBookings,
			dbx.HashExp{"host": h},
			dbx.NewExp("status IN ("+state.ActiveStatusSQLList()+")"),
			dbx.NewExp("start_utc >= {:from} AND start_utc < {:to}",
				dbx.Params{"from": from, "to": to}),
		)
		if err != nil {
			return nil, err
		}
		out[h] = len(recs)
	}
	return out, nil
}

// ActiveBusyAllHostsIntersection returns the busy windows that ALL given
// hosts share — i.e. when a slot is offered only if at least one host is
// free. For v1.1 pooled-availability event-types.
func (r *Repo) ActiveBusyAllHostsIntersection(hostIDs []string, from, to time.Time) ([]timeutil.Interval, error) {
	if len(hostIDs) == 0 {
		return nil, nil
	}
	// Compute each host's busy set, then intersect by walking the timeline.
	busyPerHost := make([][]timeutil.Interval, 0, len(hostIDs))
	for _, h := range hostIDs {
		b, err := r.ActiveBusyForHost(h, from, to)
		if err != nil {
			return nil, err
		}
		busyPerHost = append(busyPerHost, b)
	}
	// Start with first host's busy set, then intersect with each subsequent.
	cur := busyPerHost[0]
	for i := 1; i < len(busyPerHost); i++ {
		cur = intersectIntervals(cur, busyPerHost[i])
	}
	return cur, nil
}

// intersectIntervals returns the overlapping pieces of two sorted-or-not
// interval lists. O(n*m) — fine for the pool sizes we expect (<10 hosts).
func intersectIntervals(a, b []timeutil.Interval) []timeutil.Interval {
	out := []timeutil.Interval{}
	for _, x := range a {
		for _, y := range b {
			lo := x.Start
			if y.Start.After(lo) {
				lo = y.Start
			}
			hi := x.End
			if y.End.Before(hi) {
				hi = y.End
			}
			if lo.Before(hi) {
				out = append(out, timeutil.Interval{Start: lo, End: hi})
			}
		}
	}
	return out
}

// ActiveBusyForHosts is the multi-host variant of ActiveBusyForHost,
// returning the union of busy intervals across a pool. Used by the slot
// calculator when an event-type has a hosts[] pool: a slot is offered when
// at least one host is free at that time.
func (r *Repo) ActiveBusyForHosts(hostIDs []string, from, to time.Time) ([]timeutil.Interval, error) {
	out := []timeutil.Interval{}
	for _, h := range hostIDs {
		busy, err := r.ActiveBusyForHost(h, from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, busy...)
	}
	return out, nil
}

// SetBookingApproval flips a pending_approval booking to confirmed (or
// pending_payment if the event-type is paid) and rotates the approval-token.
// Returns the updated booking.
func (r *Repo) SetBookingApproval(bookingID, approverID string) (Booking, error) {
	var out Booking
	err := r.app.RunInTransaction(func(txApp core.App) error {
		rec, err := txApp.FindRecordById(migrations.CollBookings, bookingID)
		if err != nil {
			return err
		}
		from := state.Status(rec.GetString("status"))
		if from != state.StatusPendingApproval {
			return fmt.Errorf("booking is %q, not pending_approval", from)
		}
		// Move to confirmed by default; pipeline layer corrects to
		// pending_payment when the event-type is paid.
		target := state.StatusConfirmed
		if err := state.Transition(from, target); err != nil {
			return err
		}
		rec.Set("status", string(target))
		rec.Set("approved_at", mustDateTime(time.Now().UTC()))
		if approverID != "" {
			rec.Set("approved_by", approverID)
		}
		// Invalidate the approval token (single-use).
		rec.Set("approval_token_hash", "")
		if err := txApp.Save(rec); err != nil {
			return err
		}
		out = recordToBooking(rec)
		return nil
	})
	return out, err
}

// SetBookingDeclined finishes the approval flow with a "no". Always lands
// in cancelled.
func (r *Repo) SetBookingDeclined(bookingID, reason string) (Booking, error) {
	var out Booking
	err := r.app.RunInTransaction(func(txApp core.App) error {
		rec, err := txApp.FindRecordById(migrations.CollBookings, bookingID)
		if err != nil {
			return err
		}
		from := state.Status(rec.GetString("status"))
		if err := state.Transition(from, state.StatusCancelled); err != nil {
			return err
		}
		rec.Set("status", string(state.StatusCancelled))
		rec.Set("declined_at", mustDateTime(time.Now().UTC()))
		rec.Set("declined_reason", reason)
		rec.Set("approval_token_hash", "")
		if err := txApp.Save(rec); err != nil {
			return err
		}
		out = recordToBooking(rec)
		return nil
	})
	return out, err
}

// SetApprovalTokenHash stores the hash for the host's approve/decline link.
func (r *Repo) SetApprovalTokenHash(bookingID, hash string) error {
	rec, err := r.app.FindRecordById(migrations.CollBookings, bookingID)
	if err != nil {
		return err
	}
	rec.Set("approval_token_hash", hash)
	return r.app.Save(rec)
}

// FindBookingByPaymentExternalID looks up a booking by its Stripe/PayPal
// session ID. Used by inbound webhook events that don't carry our own
// client_reference_id (e.g. charge.refunded — P3-I10).
func (r *Repo) FindBookingByPaymentExternalID(externalID string) (Booking, error) {
	rec, err := r.app.FindFirstRecordByFilter(migrations.CollBookings,
		"payment_external_id = {:id}", dbx.Params{"id": externalID})
	if err != nil {
		return Booking{}, err
	}
	return recordToBooking(rec), nil
}

func (r *Repo) FindBookingByID(id string) (Booking, error) {
	rec, err := r.app.FindRecordById(migrations.CollBookings, id)
	if err != nil {
		return Booking{}, err
	}
	return recordToBooking(rec), nil
}

// ActiveBusyForHost returns the in-flight bookings for a host within [from, to]
// as busy intervals. Used as input to slot.ComputeSlots and as the
// conflict-check source for INV-1.
//
// The active-status list is hard-coded into the SQL because dbx's parameter
// binding does not expand slices into IN clauses. If we add a new active
// status, update state.ActiveStatuses() AND this string.
func (r *Repo) ActiveBusyForHost(hostID string, from, to time.Time) ([]timeutil.Interval, error) {
	recs, err := r.app.FindAllRecords(migrations.CollBookings,
		dbx.HashExp{"host": hostID},
		dbx.NewExp("status IN ("+state.ActiveStatusSQLList()+")"),
		dbx.NewExp("end_utc > {:from} AND start_utc < {:to}", dbx.Params{"from": from, "to": to}),
	)
	if err != nil {
		return nil, err
	}
	out := make([]timeutil.Interval, 0, len(recs))
	for _, rec := range recs {
		out = append(out, timeutil.Interval{
			Start: rec.GetDateTime("start_utc").Time(),
			End:   rec.GetDateTime("end_utc").Time(),
		})
	}
	return out, nil
}

// ErrSlotTaken is returned by ReserveBookingTx when an existing active
// booking overlaps the requested slot.
var ErrSlotTaken = errors.New("slot taken")

// BookingDraft is the input to ReserveBookingTx. Pricing/meeting/calendar
// fields are set by subsequent pipeline steps.
type BookingDraft struct {
	EventTypeID         string
	HostID              string
	StartUTC            time.Time
	EndUTC              time.Time
	InviteeName         string
	InviteeEmail        string
	InviteePhone        string
	InviteeTimezone     string
	IntakeData          map[string]any
	IntakeSchemaVersion int
	Status              state.Status
	PaymentStatus       string

	// v1.1: group-event sessions share the same id across attendees of the
	// same slot (event_id + start_utc). Empty for single-attendee events.
	GroupSessionID string

	// Capacity lets ReserveBookingTx enforce group capacity INSIDE the
	// transaction (uses EventTypeID above). Capacity <= 1 = single-attendee
	// (host-overlap check applies instead).
	Capacity int
}

// ErrCapacityFull is returned by ReserveBookingTx when a group slot is full.
var ErrCapacityFull = errors.New("slot capacity full")

// ReserveBookingTx enforces INV-1 inside a transaction: re-query active
// bookings for the host and slot window, and fail if any overlap. If clear,
// insert the new booking. Returns the persisted Booking.
//
// v1.1 group events (Capacity > 1) skip the host-overlap check but must not
// exceed Capacity. The count is done INSIDE the tx AFTER the insert: the
// INSERT takes SQLite's write lock, so concurrent reservations serialize and
// each sees the committed rows of the ones before it — closing the TOCTOU
// where a pre-insert count let two racers both take the last seat (2026-07-16).
func (r *Repo) ReserveBookingTx(draft BookingDraft) (Booking, error) {
	var out Booking
	err := r.app.RunInTransaction(func(txApp core.App) error {
		if draft.GroupSessionID == "" {
			tx := r.WithTx(txApp)
			busy, err := tx.ActiveBusyForHost(draft.HostID, draft.StartUTC, draft.EndUTC)
			if err != nil {
				return err
			}
			newSlot := timeutil.Interval{Start: draft.StartUTC, End: draft.EndUTC}
			for _, b := range busy {
				if newSlot.Overlaps(b) {
					return ErrSlotTaken
				}
			}
		}
		coll, err := txApp.FindCollectionByNameOrId(migrations.CollBookings)
		if err != nil {
			return err
		}
		rec := core.NewRecord(coll)
		applyDraft(rec, draft)
		if err := txApp.Save(rec); err != nil {
			return fmt.Errorf("save booking: %w", err)
		}
		// Group capacity, authoritatively: our row is now written and holds the
		// write lock. Count active bookings on this slot (incl. ours) and roll
		// back if we pushed it over capacity.
		if draft.GroupSessionID != "" && draft.Capacity > 1 {
			count, err := r.WithTx(txApp).CountActiveAtSlot(draft.EventTypeID, draft.StartUTC, draft.EndUTC)
			if err != nil {
				return err
			}
			if count > draft.Capacity {
				return ErrCapacityFull
			}
		}
		out = recordToBooking(rec)
		return nil
	})
	return out, err
}

func applyDraft(rec *core.Record, d BookingDraft) {
	rec.Set("event_type", d.EventTypeID)
	rec.Set("host", d.HostID)
	rec.Set("start_utc", mustDateTime(d.StartUTC))
	rec.Set("end_utc", mustDateTime(d.EndUTC))
	rec.Set("invitee_name", d.InviteeName)
	rec.Set("invitee_email", d.InviteeEmail)
	rec.Set("invitee_phone", d.InviteePhone)
	rec.Set("invitee_timezone", d.InviteeTimezone)
	rec.Set("intake_data", d.IntakeData)
	if d.IntakeSchemaVersion > 0 {
		rec.Set("intake_schema_version", d.IntakeSchemaVersion)
	}
	rec.Set("status", string(d.Status))
	rec.Set("payment_status", d.PaymentStatus)
	if d.GroupSessionID != "" {
		rec.Set("group_session_id", d.GroupSessionID)
	}
}

// UpdateStatus changes a booking's status with state-machine validation,
// optionally setting additional fields (e.g. cancellation_reason).
func (r *Repo) UpdateStatus(id string, target state.Status, extras map[string]any) (Booking, error) {
	var out Booking
	err := r.app.RunInTransaction(func(txApp core.App) error {
		rec, err := txApp.FindRecordById(migrations.CollBookings, id)
		if err != nil {
			return err
		}
		from := state.Status(rec.GetString("status"))
		if err := state.Transition(from, target); err != nil {
			return err
		}
		rec.Set("status", string(target))
		for k, v := range extras {
			if dt, ok := v.(time.Time); ok {
				rec.Set(k, mustDateTime(dt))
				continue
			}
			rec.Set(k, v)
		}
		if err := txApp.Save(rec); err != nil {
			return err
		}
		out = recordToBooking(rec)
		return nil
	})
	return out, err
}

// SetCancelTokenHash writes a new hash and rotates the previous one.
func (r *Repo) SetCancelTokenHash(id, hash string) error {
	rec, err := r.app.FindRecordById(migrations.CollBookings, id)
	if err != nil {
		return err
	}
	rec.Set("cancel_token_hash", hash)
	return r.app.Save(rec)
}

// FindGroupMeeting returns the meeting_join_url + external_calendar_id of an
// already-confirmed booking in the same group session, so every attendee of a
// group event (a webinar) joins ONE shared meeting instead of getting their own
// isolated room (2026-07-23). found=false when this is the first booking of the
// session (the caller then creates the meeting). excludeBookingID skips the
// current row. Small race window: two truly simultaneous first bookings could
// each create a meeting — acceptable for trickle-in webinar signups.
func (r *Repo) FindGroupMeeting(groupSessionID, excludeBookingID string) (meetingURL, externalID string, found bool) {
	if groupSessionID == "" {
		return "", "", false
	}
	// dbx.NewExp (raw SQL), NOT FindRecordsByFilter: the PocketBase filter
	// language has no IN operator, so the status clause silently errored and the
	// query returned nothing → every attendee spawned its own meeting
	// (2026-07-23). This matches the working CountActiveAtSlot/ActiveBusyForHost.
	recs, err := r.app.FindAllRecords(migrations.CollBookings,
		dbx.HashExp{"group_session_id": groupSessionID},
		dbx.NewExp("id != {:excl}", dbx.Params{"excl": excludeBookingID}),
		dbx.NewExp("meeting_join_url != ''"),
		dbx.NewExp("status IN ("+state.ActiveStatusSQLList()+")"),
	)
	if err != nil || len(recs) == 0 {
		return "", "", false
	}
	rec := recs[0]
	return rec.GetString("meeting_join_url"), rec.GetString("external_calendar_id"), true
}

// PersistBookingExternals stores the per-booking calendar/meeting external IDs.
// Called from the pipeline confirm-tail.
func (r *Repo) PersistBookingExternals(bookingID, externalCalendarID, provider, meetingURL string) error {
	rec, err := r.app.FindRecordById(migrations.CollBookings, bookingID)
	if err != nil {
		return err
	}
	if externalCalendarID != "" {
		rec.Set("external_calendar_id", externalCalendarID)
	}
	if provider != "" {
		rec.Set("external_calendar_provider", provider)
	}
	if meetingURL != "" {
		rec.Set("meeting_join_url", meetingURL)
	}
	return r.app.Save(rec)
}

// SetCalendarIntegrationError persists msg to last_error on the owner's
// calendar-family integration rows (msgraph/nextcloud/google), so a failing
// meeting/calendar sync is visible outside the logs. Empty msg clears the
// field. Called best-effort from the pipeline confirm-tail (2026-07-16 —
// before this, decrypt/create failures were invisible: bookings confirmed
// silently without a meeting).
func (r *Repo) SetCalendarIntegrationError(ownerID, msg string) error {
	recs, err := r.app.FindRecordsByFilter(migrations.CollIntegrations,
		"owner = {:owner} && (provider = 'msgraph' || provider = 'microsoft' || provider = 'nextcloud' || provider = 'google')",
		"", 10, 0, dbx.Params{"owner": ownerID})
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.GetString("last_error") == msg {
			continue
		}
		rec.Set("last_error", msg)
		if err := r.app.Save(rec); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceStartEnd is used by reschedule; it updates start/end + bumps token.
func (r *Repo) ReplaceStartEnd(id string, newStart, newEnd time.Time) (Booking, error) {
	var out Booking
	err := r.app.RunInTransaction(func(txApp core.App) error {
		rec, err := txApp.FindRecordById(migrations.CollBookings, id)
		if err != nil {
			return err
		}
		host := rec.GetString("host")
		bookingID := rec.Id
		// re-check for conflicts EXCLUDING ourselves
		busy, err := r.WithTx(txApp).ActiveBusyForHost(host, newStart, newEnd)
		if err != nil {
			return err
		}
		newSlot := timeutil.Interval{Start: newStart, End: newEnd}
		for _, b := range busy {
			if newSlot.Overlaps(b) {
				// the only legitimate self-overlap is when we accidentally
				// matched our own row — but ActiveBusyForHost doesn't filter
				// by ID. We protect by comparing the booking ID via a probe.
				// Simplest: skip self-matches by reloading the candidate's id.
				continue
			}
		}
		// re-query active bookings excluding self
		recs, err := txApp.FindAllRecords(migrations.CollBookings,
			dbx.HashExp{"host": host},
			dbx.NewExp("id != {:self}", dbx.Params{"self": bookingID}),
			dbx.NewExp("status IN ("+state.ActiveStatusSQLList()+")"),
			dbx.NewExp("end_utc > {:from} AND start_utc < {:to}", dbx.Params{"from": newStart, "to": newEnd}),
		)
		if err != nil {
			return err
		}
		for _, other := range recs {
			iv := timeutil.Interval{Start: other.GetDateTime("start_utc").Time(), End: other.GetDateTime("end_utc").Time()}
			if newSlot.Overlaps(iv) {
				return ErrSlotTaken
			}
		}
		rec.Set("start_utc", mustDateTime(newStart))
		rec.Set("end_utc", mustDateTime(newEnd))
		if err := txApp.Save(rec); err != nil {
			return err
		}
		out = recordToBooking(rec)
		return nil
	})
	return out, err
}

// FindIntegrationCredentials returns the encrypted credentials blob, the
// (unencrypted) JSON config, and a found-flag for the given host+provider.
// Used by the adapter Registry.
func (r *Repo) FindIntegrationCredentials(hostID, provider string) (string, []byte, bool, error) {
	rec, err := r.app.FindFirstRecordByFilter(
		migrations.CollIntegrations,
		"owner = {:owner} && provider = {:provider} && sync_enabled = true",
		dbx.Params{"owner": hostID, "provider": provider},
	)
	if err != nil {
		// Only a genuine "no such record" means "no integration"; a real
		// store/query error MUST propagate, otherwise a fail-closed caller
		// (booking validation) would silently proceed as if the host had no
		// calendar and skip the external conflict check.
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, false, nil
		}
		return "", nil, false, err
	}
	creds := rec.GetString("credentials")
	conf := []byte(rec.GetString("config"))
	if len(conf) == 0 {
		if v := rec.Get("config"); v != nil {
			if jr, ok := v.(types.JSONRaw); ok {
				conf = []byte(jr)
			}
		}
	}
	return creds, conf, true, nil
}

// UpdateIntegrationCredentials replaces the encrypted credentials blob on a
// host's active integration row for a provider, but only if the stored blob is
// still expectedEnc — an atomic compare-and-set. Used to persist rotated OAuth
// refresh tokens (see adapters.CredentialRotator) so a long-lived integration
// survives token rotation and process restarts. The CAS prevents a stale
// provider instance (e.g. one loaded before a manual reconnect) from clobbering
// freshly-stored credentials: if another writer changed the blob in the
// meantime, the conditional UPDATE matches no row and persisted is false. Both
// blobs must already be encrypted by the caller (crypto.Master.Encrypt).
func (r *Repo) UpdateIntegrationCredentials(hostID, provider, expectedEnc, newEnc string) (persisted bool, err error) {
	rec, err := r.app.FindFirstRecordByFilter(
		migrations.CollIntegrations,
		"owner = {:owner} && provider = {:provider} && sync_enabled = true",
		dbx.Params{"owner": hostID, "provider": provider},
	)
	if err != nil {
		return false, err
	}
	// Conditional UPDATE ... WHERE id = ? AND credentials = ? — the DB re-checks
	// the expected ciphertext atomically, so there is no find→save TOCTOU. A
	// raw update skips the `updated` autodate bump, which is immaterial for a
	// silent token rotation.
	res, err := r.app.DB().Update(
		migrations.CollIntegrations,
		dbx.Params{"credentials": newEnc},
		dbx.HashExp{"id": rec.Id, "credentials": expectedEnc},
	).Execute()
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// calendarFamilyProviders are the providers that map onto a host's ONE external
// calendar. Only a single one may be sync_enabled at a time — the adapter
// registry refuses to guess between several active ones (see
// registry.loadIntegration; keep this list in sync with it).
var calendarFamilyProviders = map[string]bool{
	"msgraph": true, "microsoft": true, "nextcloud": true, "google": true,
}

// DeactivateOtherCalendarIntegrations disables (sync_enabled=false) every other
// active calendar-family integration for hostID, keeping only keepProvider.
// Call it whenever a calendar provider is connected/enabled: without it a host
// who wires up a second calendar ends up with two active rows, and
// CalendarForHost then hard-errors — which fails booking validation closed
// (ErrExternalCalendarUnavailable). "Most recently connected wins."
func (r *Repo) DeactivateOtherCalendarIntegrations(hostID, keepProvider string) error {
	recs, err := r.app.FindAllRecords(migrations.CollIntegrations,
		dbx.HashExp{"owner": hostID},
	)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		prov := rec.GetString("provider")
		if prov == keepProvider || !rec.GetBool("sync_enabled") || !calendarFamilyProviders[prov] {
			continue
		}
		rec.Set("sync_enabled", false)
		if err := r.app.Save(rec); err != nil {
			return err
		}
	}
	return nil
}

// ----- service_tokens -----

type ServiceTokenRecord struct {
	ID                 string
	Name               string
	Scopes             []string
	HostBinding        string
	EventTypeAllowlist []string
	CreatedBy          string
	ExpiresAt          time.Time
	LastUsedAt         time.Time
	RevokedAt          time.Time
}

func (r *Repo) FindServiceTokenByHash(hash string) (ServiceTokenRecord, bool, error) {
	rec, err := r.app.FindFirstRecordByFilter(migrations.CollServiceTokens,
		"token_hash = {:h}", dbx.Params{"h": hash})
	if err != nil || rec == nil {
		return ServiceTokenRecord{}, false, nil
	}
	out := ServiceTokenRecord{
		ID:          rec.Id,
		Name:        rec.GetString("name"),
		Scopes:      rec.GetStringSlice("scopes"),
		HostBinding: rec.GetString("host_binding"),
		CreatedBy:   rec.GetString("created_by"),
		ExpiresAt:   rec.GetDateTime("expires_at").Time(),
		LastUsedAt:  rec.GetDateTime("last_used_at").Time(),
		RevokedAt:   rec.GetDateTime("revoked_at").Time(),
	}
	if raw := rec.Get("event_type_allowlist"); raw != nil {
		if jr, ok := raw.(types.JSONRaw); ok && len(jr) > 0 {
			_ = json.Unmarshal(jr, &out.EventTypeAllowlist)
		}
	}
	return out, true, nil
}

func (r *Repo) TouchServiceTokenLastUsed(id string) error {
	rec, err := r.app.FindRecordById(migrations.CollServiceTokens, id)
	if err != nil {
		return err
	}
	rec.Set("last_used_at", mustDateTime(time.Now().UTC()))
	return r.app.Save(rec)
}

// CreateServiceToken inserts a new row with the token hash and returns the
// stored record. The caller supplied the raw token to the user once.
func (r *Repo) CreateServiceToken(name, hash, createdBy string, scopes []string,
	hostBinding string, eventTypeAllowlist []string, expiresAt time.Time) (ServiceTokenRecord, error) {
	coll, err := r.app.FindCollectionByNameOrId(migrations.CollServiceTokens)
	if err != nil {
		return ServiceTokenRecord{}, err
	}
	rec := core.NewRecord(coll)
	rec.Set("name", name)
	rec.Set("token_hash", hash)
	rec.Set("scopes", scopes)
	if hostBinding != "" {
		rec.Set("host_binding", hostBinding)
	}
	if len(eventTypeAllowlist) > 0 {
		rec.Set("event_type_allowlist", eventTypeAllowlist)
	}
	rec.Set("created_by", createdBy)
	if !expiresAt.IsZero() {
		rec.Set("expires_at", mustDateTime(expiresAt))
	}
	if err := r.app.Save(rec); err != nil {
		return ServiceTokenRecord{}, err
	}
	return ServiceTokenRecord{
		ID: rec.Id, Name: name, Scopes: scopes,
		HostBinding: hostBinding, EventTypeAllowlist: eventTypeAllowlist,
		ExpiresAt: expiresAt, CreatedBy: createdBy,
	}, nil
}

func (r *Repo) ListServiceTokens() ([]ServiceTokenRecord, error) {
	recs, err := r.app.FindAllRecords(migrations.CollServiceTokens)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceTokenRecord, 0, len(recs))
	for _, rec := range recs {
		t := ServiceTokenRecord{
			ID:          rec.Id,
			Name:        rec.GetString("name"),
			Scopes:      rec.GetStringSlice("scopes"),
			HostBinding: rec.GetString("host_binding"),
			CreatedBy:   rec.GetString("created_by"),
			ExpiresAt:   rec.GetDateTime("expires_at").Time(),
			LastUsedAt:  rec.GetDateTime("last_used_at").Time(),
			RevokedAt:   rec.GetDateTime("revoked_at").Time(),
		}
		if raw := rec.Get("event_type_allowlist"); raw != nil {
			if jr, ok := raw.(types.JSONRaw); ok && len(jr) > 0 {
				_ = json.Unmarshal(jr, &t.EventTypeAllowlist)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *Repo) RevokeServiceToken(id string) error {
	rec, err := r.app.FindRecordById(migrations.CollServiceTokens, id)
	if err != nil {
		return err
	}
	rec.Set("revoked_at", mustDateTime(time.Now().UTC()))
	return r.app.Save(rec)
}

func (r *Repo) DeleteServiceToken(id string) error {
	rec, err := r.app.FindRecordById(migrations.CollServiceTokens, id)
	if err != nil {
		return err
	}
	return r.app.Delete(rec)
}

// FindIntegrationByID returns the (provider, owner) tuple for an integration
// record. Used by the outbound-webhook delivery path when retrying.
func (r *Repo) FindIntegrationByID(id string) (provider, ownerID string, err error) {
	rec, err := r.app.FindRecordById(migrations.CollIntegrations, id)
	if err != nil {
		return "", "", err
	}
	return rec.GetString("provider"), rec.GetString("owner"), nil
}

// ----- outbound webhooks -----

// OutboundWebhook is the configured subscriber.
type OutboundWebhook struct {
	ID     string
	Owner  string
	URL    string
	Secret string
	Events []string
	Active bool
}

func (r *Repo) ActiveOutboundWebhooksForEvent(ownerID, event string) ([]OutboundWebhook, error) {
	// Owner null = global webhook; owner set = host-scoped.
	recs, err := r.app.FindAllRecords(migrations.CollOutboundWebhooks,
		dbx.NewExp("active = true"),
		dbx.NewExp("owner = {:owner} OR owner = ''", dbx.Params{"owner": ownerID}),
	)
	if err != nil {
		return nil, err
	}
	out := make([]OutboundWebhook, 0, len(recs))
	for _, rec := range recs {
		var events []string
		if raw := rec.Get("events"); raw != nil {
			if jr, ok := raw.(types.JSONRaw); ok {
				_ = json.Unmarshal(jr, &events)
			}
		}
		match := false
		for _, e := range events {
			if e == event || e == "*" {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		out = append(out, OutboundWebhook{
			ID:     rec.Id,
			Owner:  rec.GetString("owner"),
			URL:    rec.GetString("url"),
			Secret: rec.GetString("secret"),
			Events: events,
			Active: rec.GetBool("active"),
		})
	}
	return out, nil
}

// WebhookDelivery is one delivery attempt row in webhook_deliveries.
type WebhookDelivery struct {
	ID                 string
	WebhookID          string
	EventID            string
	Payload            []byte
	Status             string // pending|delivered|failed|abandoned
	Attempts           int
	NextRetryAt        time.Time
	LastResponseStatus int
}

func (r *Repo) EnqueueWebhookDelivery(webhookID, eventID string, payload []byte) error {
	coll, err := r.app.FindCollectionByNameOrId(migrations.CollWebhookDeliveries)
	if err != nil {
		return err
	}
	// Idempotency: skip if (webhook, event_id) already exists.
	existing, _ := r.app.FindFirstRecordByFilter(
		migrations.CollWebhookDeliveries,
		"webhook = {:wh} && event_id = {:ev}",
		dbx.Params{"wh": webhookID, "ev": eventID},
	)
	if existing != nil {
		return nil
	}
	rec := core.NewRecord(coll)
	rec.Set("webhook", webhookID)
	rec.Set("event_id", eventID)
	rec.Set("payload", json.RawMessage(payload))
	rec.Set("status", "pending")
	rec.Set("attempts", 0)
	rec.Set("next_retry_at", mustDateTime(time.Now().UTC()))
	return r.app.Save(rec)
}

// PendingWebhookDeliveries returns deliveries ready to attempt.
func (r *Repo) PendingWebhookDeliveries(limit int) ([]WebhookDelivery, error) {
	recs, err := r.app.FindAllRecords(migrations.CollWebhookDeliveries,
		dbx.NewExp("status IN ('pending','failed')"),
		dbx.NewExp("next_retry_at <= {:now}", dbx.Params{"now": time.Now().UTC()}),
	)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(recs) > limit {
		recs = recs[:limit]
	}
	out := make([]WebhookDelivery, 0, len(recs))
	for _, rec := range recs {
		var payload []byte
		if raw := rec.Get("payload"); raw != nil {
			if jr, ok := raw.(types.JSONRaw); ok {
				payload = []byte(jr)
			}
		}
		out = append(out, WebhookDelivery{
			ID:                 rec.Id,
			WebhookID:          rec.GetString("webhook"),
			EventID:            rec.GetString("event_id"),
			Payload:            payload,
			Status:             rec.GetString("status"),
			Attempts:           rec.GetInt("attempts"),
			LastResponseStatus: rec.GetInt("last_response_status"),
		})
	}
	return out, nil
}

func (r *Repo) UpdateWebhookDeliveryResult(id string, status string, attempts int, lastResponseStatus int, nextRetryAt time.Time) error {
	rec, err := r.app.FindRecordById(migrations.CollWebhookDeliveries, id)
	if err != nil {
		return err
	}
	rec.Set("status", status)
	rec.Set("attempts", attempts)
	rec.Set("last_response_status", lastResponseStatus)
	if !nextRetryAt.IsZero() {
		rec.Set("next_retry_at", mustDateTime(nextRetryAt))
	}
	return r.app.Save(rec)
}

func (r *Repo) FindOutboundWebhookByID(id string) (OutboundWebhook, error) {
	rec, err := r.app.FindRecordById(migrations.CollOutboundWebhooks, id)
	if err != nil {
		return OutboundWebhook{}, err
	}
	var events []string
	if raw := rec.Get("events"); raw != nil {
		if jr, ok := raw.(types.JSONRaw); ok {
			_ = json.Unmarshal(jr, &events)
		}
	}
	return OutboundWebhook{
		ID: rec.Id, Owner: rec.GetString("owner"), URL: rec.GetString("url"),
		Secret: rec.GetString("secret"), Events: events, Active: rec.GetBool("active"),
	}, nil
}

// ----- webhook idempotency for inbound payment events -----

// HasProcessedPaymentEvent returns true if the provider event-id has already
// been recorded. We piggy-back on the audit_log: action="webhook.processed",
// target_id=event_id. Cheap, persistent, and lets us inspect history.
func (r *Repo) HasProcessedPaymentEvent(provider, eventID string) (bool, error) {
	rec, _ := r.app.FindFirstRecordByFilter(migrations.CollAuditLog,
		"action = 'webhook.processed' && target_type = {:t} && target_id = {:id}",
		dbx.Params{"t": provider, "id": eventID})
	return rec != nil, nil
}

func (r *Repo) MarkPaymentEventProcessed(provider, eventID, bookingID string, meta map[string]any) error {
	return r.WriteAudit(AuditEntry{
		Actor: "system", Action: "webhook.processed",
		TargetType: provider, TargetID: eventID,
		Metadata: meta,
	})
}

// SetBookingPaymentResult is called after a successful inbound payment event.
func (r *Repo) SetBookingPaymentResult(bookingID string, status state.Status, paymentStatus, externalID string, amountCents int) (Booking, error) {
	rec, err := r.app.FindRecordById(migrations.CollBookings, bookingID)
	if err != nil {
		return Booking{}, err
	}
	from := state.Status(rec.GetString("status"))
	if from != status {
		if err := state.Transition(from, status); err != nil {
			return Booking{}, err
		}
		rec.Set("status", string(status))
	}
	rec.Set("payment_status", paymentStatus)
	if externalID != "" {
		rec.Set("payment_external_id", externalID)
	}
	if amountCents > 0 {
		rec.Set("payment_amount_paid", amountCents)
	}
	if err := r.app.Save(rec); err != nil {
		return Booking{}, err
	}
	return recordToBooking(rec), nil
}

// AuditEntry is the minimal payload for an audit_log insert.
type AuditEntry struct {
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	IP         string
	Metadata   map[string]any
}

func (r *Repo) WriteAudit(e AuditEntry) error {
	coll, err := r.app.FindCollectionByNameOrId(migrations.CollAuditLog)
	if err != nil {
		return err
	}
	rec := core.NewRecord(coll)
	rec.Set("actor", e.Actor)
	rec.Set("action", e.Action)
	rec.Set("target_type", e.TargetType)
	rec.Set("target_id", e.TargetID)
	rec.Set("ip", e.IP)
	rec.Set("metadata", e.Metadata)
	return r.app.Save(rec)
}

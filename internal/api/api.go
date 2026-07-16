// Package api wires the public booking API (/api/public/v1/...) onto
// PocketBase's router. Routes here are intentionally thin: they parse input,
// call the pipeline / repo, and translate domain errors into the Doc-06
// error JSON shape.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"

	"github.com/qognio/qognical/internal/crypto"
	"github.com/qognio/qognical/internal/i18n"
	"github.com/qognio/qognical/internal/intake"
	"github.com/qognio/qognical/internal/pipeline"
	"github.com/qognio/qognical/internal/slot"
	"github.com/qognio/qognical/internal/state"
	"github.com/qognio/qognical/internal/store"
	"github.com/qognio/qognical/internal/svctoken"
	"github.com/qognio/qognical/internal/timeutil"
	"github.com/qognio/qognical/internal/token"
)

// API bundles the dependencies used by the route handlers.
type API struct {
	Repo               *store.Repo
	Tokens             *token.Service
	Pipeline           *pipeline.Pipeline
	Master             *crypto.Master // encrypts integration credentials at rest
	CORSAllowedOrigins []string
	Captcha            CaptchaVerifier // optional; nil = no captcha enforcement
	ReadLimiter        RateLimiter     // optional; nil = no limit
	MutationLimiter    RateLimiter
}

// RateLimiter is the subset of internal/ratelimit we depend on (avoids
// import cycles).
type RateLimiter interface {
	Allow(key string) (bool, time.Duration)
}

// CaptchaVerifier is the subset of internal/captcha we depend on (interface
// in api keeps the import direction clean).
type CaptchaVerifier interface {
	Verify(ctx string, token string, remoteIP string) error
}

// Register attaches routes to the given ServeEvent router.
func (a *API) Register(se *core.ServeEvent) {
	g := se.Router.Group("/api/public/v1")
	g.BindFunc(a.corsMiddleware)
	g.BindFunc(a.rateLimitMiddleware)

	g.GET("/labels", a.handleLabels)
	g.GET("/event-types", a.handleListEventTypes) // directory for root landing
	g.GET("/event-types/{host}/{slug}", a.handleGetEventType)
	g.GET("/event-types/{host}/{slug}/slots", a.handleListSlots)
	g.POST("/bookings", a.handleCreateBooking)
	// Token-bearing routes: keep the ?token=... out of PocketBase's activity
	// log so a bearer-capable booking token can't leak via the log DB
	// (2026-07-16). The token is also transported/echoed with no-store.
	g.POST("/bookings/{id}/cancel", a.handleCancel).Bind(apis.SkipSuccessActivityLog())
	g.POST("/bookings/{id}/reschedule", a.handleReschedule).Bind(apis.SkipSuccessActivityLog())
	g.GET("/bookings/{id}", a.handleGetBooking).Bind(apis.SkipSuccessActivityLog())
	// v1.1 host approval — GET renders a confirmation page, POST mutates.
	g.GET("/bookings/{id}/approve", a.handleApprove).Bind(apis.SkipSuccessActivityLog())
	g.POST("/bookings/{id}/approve", a.handleApprove).Bind(apis.SkipSuccessActivityLog())
	g.GET("/bookings/{id}/decline", a.handleDecline).Bind(apis.SkipSuccessActivityLog())
	g.POST("/bookings/{id}/decline", a.handleDecline).Bind(apis.SkipSuccessActivityLog())
	g.OPTIONS("/{path...}", func(e *core.RequestEvent) error { return e.NoContent(http.StatusNoContent) })
}

// ----- middleware -----

// rateLimitMiddleware enforces per-IP rate limits per Doc 06. Different
// buckets for reads (lenient) and mutations (strict). A VALID service token
// bypasses — it has its own quota.
//
// SECURITY (2026-07-16): the bypass previously fired for ANY non-empty
// Authorization header, so `Authorization: x` lifted the IP quota entirely
// (unbounded scraping of expensive slot/directory reads, and the booking POST
// body was decoded before the limiter). We now bypass only after the token
// actually verifies as a service token; anything else is rate-limited normally.
func (a *API) rateLimitMiddleware(e *core.RequestEvent) error {
	if hdr := e.Request.Header.Get("Authorization"); hdr != "" {
		if _, err := svctoken.Verify(a.Repo, hdr); err == nil {
			return e.Next() // genuine service token → its own quota applies
		}
		// invalid/garbage Authorization → fall through to normal IP limiting
	}
	method := e.Request.Method
	var limiter RateLimiter
	if method == "GET" || method == "HEAD" || method == "OPTIONS" {
		limiter = a.ReadLimiter
	} else {
		limiter = a.MutationLimiter
	}
	if limiter == nil {
		return e.Next()
	}
	ok, retry := limiter.Allow(e.RealIP())
	if !ok {
		e.Response.Header().Set("Retry-After",
			fmt.Sprintf("%d", int(retry.Seconds())+1))
		return writeErr(e, http.StatusTooManyRequests, CodeRateLimitExceeded,
			"rate limit exceeded", nil)
	}
	return e.Next()
}

func (a *API) corsMiddleware(e *core.RequestEvent) error {
	origin := e.Request.Header.Get("Origin")
	if origin != "" && a.allowedOrigin(origin) {
		e.Response.Header().Set("Access-Control-Allow-Origin", origin)
		e.Response.Header().Set("Vary", "Origin")
		e.Response.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		e.Response.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Booking-Token, Authorization")
		e.Response.Header().Set("Access-Control-Max-Age", "3600")
	}
	return e.Next()
}

func (a *API) allowedOrigin(o string) bool {
	for _, allowed := range a.CORSAllowedOrigins {
		if allowed == o {
			return true
		}
	}
	return false
}

// ----- GET /labels (i18n bundle for SPA) -----

func (a *API) handleLabels(e *core.RequestEvent) error {
	e.Response.Header().Set("Cache-Control", "public, max-age=600")
	return e.JSON(http.StatusOK, i18n.All())
}

// ----- GET /event-types/{host}/{slug} -----

type eventTypeView struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Description     string `json:"description,omitempty"`
	DurationMin     int    `json:"duration_min"`
	LocationType    string `json:"location_type"`
	IntakeSchema    any    `json:"intake_schema,omitempty"`
	PaymentMode     string `json:"payment_mode"`
	PaymentAmount   int    `json:"payment_amount,omitempty"`
	PaymentCurrency string `json:"payment_currency,omitempty"`
	HostName        string `json:"host_name,omitempty"`
	HostTimezone    string `json:"host_timezone,omitempty"`

	// v1.1: branding + capacity + approval + locale, all surface to the SPA.
	BrandColor       string `json:"brand_color,omitempty"`
	BrandLogoURL     string `json:"brand_logo_url,omitempty"`
	CustomCSS        any    `json:"custom_css,omitempty"`
	Capacity         int    `json:"capacity,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
	Locale           string `json:"locale,omitempty"`
	PoolSize         int    `json:"pool_size,omitempty"`
}

// ----- GET /event-types (directory for the root landing) -----

type eventTypeListItem struct {
	ID              string `json:"id"`
	Slug            string `json:"slug"`
	Title           string `json:"title"`
	DurationMin     int    `json:"duration_min"`
	LocationType    string `json:"location_type,omitempty"`
	PaymentMode     string `json:"payment_mode,omitempty"`
	PaymentAmount   int    `json:"payment_amount,omitempty"`
	PaymentCurrency string `json:"payment_currency,omitempty"`
	BrandColor      string `json:"brand_color,omitempty"`
	HostName        string `json:"host_name,omitempty"`
	HostSlug        string `json:"host_slug"`
}

// handleListEventTypes returns every active event-type with its host's
// public identity. Used by the root landing page directory. The host slug
// is the email value as PB stores it — same shape the SPA routes accept.
func (a *API) handleListEventTypes(e *core.RequestEvent) error {
	ets, err := a.Repo.ListActiveEventTypes()
	if err != nil {
		return internalErrLog(e, err, "ListActiveEventTypes")
	}
	out := make([]eventTypeListItem, 0, len(ets))
	for _, et := range ets {
		host, herr := a.Repo.FindHostByID(et.OwnerID)
		if herr != nil {
			continue
		}
		out = append(out, eventTypeListItem{
			ID: et.ID, Slug: et.Slug, Title: et.Title,
			DurationMin: et.DurationMin, LocationType: et.LocationType,
			PaymentMode: et.PaymentMode, PaymentAmount: et.PaymentAmount,
			PaymentCurrency: et.PaymentCurrency, BrandColor: et.BrandColor,
			HostName: host.Name, HostSlug: hostPublicSlug(host),
		})
	}
	return e.JSON(http.StatusOK, map[string]any{"event_types": out})
}

func (a *API) handleGetEventType(e *core.RequestEvent) error {
	hostSlug := e.Request.PathValue("host")
	etSlug := e.Request.PathValue("slug")
	host, err := a.findHostBySlug(hostSlug)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "host not found", nil)
	}
	et, err := a.Repo.FindEventType(host.ID, etSlug)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "event_type not found", nil)
	}
	if !et.Active {
		return writeErr(e, httpStatusFor(CodeEventTypeInactive), CodeEventTypeInactive,
			"event type is no longer accepting bookings", nil)
	}
	schemaOut, _ := decodeJSONIfPresent(et.IntakeSchema)
	cssOut, _ := decodeJSONIfPresent(et.CustomCSS)
	return e.JSON(http.StatusOK, eventTypeView{
		ID:              et.ID,
		Title:           et.Title,
		Description:     et.Description,
		DurationMin:     et.DurationMin,
		LocationType:    et.LocationType,
		IntakeSchema:    schemaOut,
		PaymentMode:     et.PaymentMode,
		PaymentAmount:   et.PaymentAmount,
		PaymentCurrency: et.PaymentCurrency,
		HostName:        host.Name,
		HostTimezone:    host.Timezone,

		BrandColor:       et.BrandColor,
		BrandLogoURL:     et.BrandLogoURL,
		CustomCSS:        cssOut,
		Capacity:         et.EffectiveCapacity(),
		RequiresApproval: et.RequiresApproval,
		Locale:           et.Locale,
		PoolSize:         len(et.AllHosts()),
	})
}

// ----- GET /event-types/{host}/{slug}/slots -----

type slotsView struct {
	Slots []slotEntry `json:"slots"`
}
type slotEntry struct {
	StartUTC time.Time `json:"start_utc"`
	EndUTC   time.Time `json:"end_utc"`
}

func (a *API) handleListSlots(e *core.RequestEvent) error {
	hostSlug := e.Request.PathValue("host")
	etSlug := e.Request.PathValue("slug")
	host, err := a.findHostBySlug(hostSlug)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "host not found", nil)
	}
	et, err := a.Repo.FindEventType(host.ID, etSlug)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "event_type not found", nil)
	}
	if !et.Active {
		return writeErr(e, httpStatusFor(CodeEventTypeInactive), CodeEventTypeInactive, "inactive", nil)
	}

	from, err := time.Parse("2006-01-02", e.Request.URL.Query().Get("from"))
	if err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "bad ?from", nil)
	}
	to, err := time.Parse("2006-01-02", e.Request.URL.Query().Get("to"))
	if err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "bad ?to", nil)
	}
	if !to.After(from) {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "to must be after from", nil)
	}
	// Hard-cap the range to discourage enumeration of months at a time.
	if to.Sub(from) > 62*24*time.Hour {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "range too wide (max 62 days)", nil)
	}

	availability, err := a.Repo.FindAvailability(host.ID)
	if err != nil {
		return internalErrLog(e, err, "FindAvailability")
	}
	overrides, err := a.Repo.FindOverridesInRange(host.ID, from, to)
	if err != nil {
		return internalErrLog(e, err, "FindOverridesInRange")
	}
	// v1.1: for pooled event-types, a slot is available when *any* host is
	// free. Subtract the per-host busy intervals from the slot grid by
	// passing the intersection of all hosts' busy windows. Simpler approach:
	// only count slot-as-busy when ALL hosts are simultaneously busy. The
	// existing single-host code path is correct when pool size = 1.
	hostIDs := et.AllHosts()
	var busy []timeutil.Interval
	if len(hostIDs) == 1 {
		busy, err = a.Repo.ActiveBusyForHost(host.ID, from, to.Add(24*time.Hour))
	} else {
		busy, err = a.Repo.ActiveBusyAllHostsIntersection(hostIDs, from, to.Add(24*time.Hour))
	}
	if err != nil {
		return internalErrLog(e, err, "ActiveBusy")
	}
	slots, errCompute := slot.ComputeSlots(slot.Input{
		EventType: slot.EventType{
			DurationMin: et.DurationMin, BufferBeforeMin: et.BufferBeforeMin,
			BufferAfterMin: et.BufferAfterMin, MinNoticeMin: et.MinNoticeMin,
			MaxHorizonDays: et.MaxHorizonDays,
		},
		HostTimezone: host.Timezone,
		Availability: availability,
		Overrides:    overrides,
		LocalBusy:    busy,
		Now:          time.Now().UTC(),
		From:         from.UTC(),
		To:           to.Add(24 * time.Hour).UTC(),
	})
	if errCompute != nil {
		return internalErrLog(e, errCompute, "ComputeSlots")
	}
	// v1.1: for group events keep a slot as long as it isn't capacity-full.
	cap := et.EffectiveCapacity()
	out := slotsView{Slots: make([]slotEntry, 0, len(slots))}
	for _, s := range slots {
		if cap > 1 {
			count, err := a.Repo.CountActiveAtSlot(et.ID, s.StartUTC, s.EndUTC)
			if err == nil && count >= cap {
				continue
			}
		}
		out.Slots = append(out.Slots, slotEntry{StartUTC: s.StartUTC, EndUTC: s.EndUTC})
	}
	return e.JSON(http.StatusOK, out)
}

// ----- POST /bookings -----

type createBookingReq struct {
	EventTypeID  string         `json:"event_type_id"`
	StartUTC     time.Time      `json:"start_utc"`
	Invitee      inviteeReq     `json:"invitee"`
	IntakeData   map[string]any `json:"intake_data"`
	CaptchaToken string         `json:"captcha_token,omitempty"`
}
type inviteeReq struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Phone    string `json:"phone,omitempty"`
	Timezone string `json:"timezone"`
}

type bookingCreatedResp struct {
	Status      string `json:"status"`
	BookingID   string `json:"booking_id"`
	CancelURL   string `json:"cancel_url,omitempty"`
	ManageURL   string `json:"manage_url,omitempty"`
	CheckoutURL string `json:"checkout_url,omitempty"`
}

// maxBookingBody caps the anonymous booking JSON (incl. intake_data) so a
// large body can't exhaust memory before validation (2026-07-16). 256 KiB is
// generous for a booking + intake payload.
const maxBookingBody = 256 << 10

func (a *API) handleCreateBooking(e *core.RequestEvent) error {
	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, maxBookingBody)
	var req createBookingReq
	if err := e.BindBody(&req); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "malformed json", nil)
	}
	if req.EventTypeID == "" || req.StartUTC.IsZero() {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "event_type_id, start_utc required", nil)
	}
	if req.Invitee.Name == "" || req.Invitee.Email == "" || req.Invitee.Timezone == "" {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "invitee.{name,email,timezone} required", nil)
	}

	// Service-token auth path: bypasses captcha + applies the token's scope
	// + host binding. ADR-0002.
	actor := "anonymous"
	var serviceToken *svctoken.Resolved
	if hdr := e.Request.Header.Get("Authorization"); hdr != "" {
		st, err := svctoken.Verify(a.Repo, hdr)
		if err != nil {
			return writeErr(e, http.StatusUnauthorized, CodeTokenInvalid, err.Error(), nil)
		}
		serviceToken = st
		if !st.HasScope(svctoken.ScopeBookingsCreate) {
			return writeErr(e, http.StatusForbidden, CodeTokenInvalid, "scope bookings:create required", nil)
		}
		// Verify host_binding + event_type_allowlist against the event-type's host.
		et, err := a.Repo.FindEventTypeByID(req.EventTypeID)
		if err != nil {
			return writeErr(e, http.StatusNotFound, CodeNotFound, "event_type not found", nil)
		}
		if !st.CanBookFor(et.OwnerID, et.ID) {
			return writeErr(e, http.StatusForbidden, CodeTokenInvalid, "token not allowed for this host/event_type", nil)
		}
		actor = "service:" + st.Token.Name
	} else if a.Captcha != nil {
		// Anonymous flow → ask the verifier. NoopVerifier (when no provider
		// configured) accepts any token including empty, so dev instances
		// work without captcha config.
		if err := a.Captcha.Verify("booking", req.CaptchaToken, e.RealIP()); err != nil {
			return writeErr(e, http.StatusBadRequest, CodeCaptchaFailed, err.Error(), nil)
		}
	}

	res, err := a.Pipeline.Run(pipeline.Request{
		EventTypeID:     req.EventTypeID,
		StartUTC:        req.StartUTC.UTC(),
		InviteeName:     req.Invitee.Name,
		InviteeEmail:    req.Invitee.Email,
		InviteePhone:    req.Invitee.Phone,
		InviteeTimezone: req.Invitee.Timezone,
		IntakeData:      req.IntakeData,
		Actor:           actor,
		IP:              e.RealIP(),
	})
	_ = serviceToken // available for future per-token logging
	if err != nil {
		return mapPipelineErr(e, err)
	}

	manage := manageURL(a.Pipeline, res.Booking.ID, res.ManageToken.String)
	resp := bookingCreatedResp{
		Status:    string(res.Booking.Status),
		BookingID: res.Booking.ID,
		ManageURL: manage,
		CancelURL: manage,
	}
	if res.CheckoutURL != "" {
		resp.CheckoutURL = res.CheckoutURL
	}
	status := http.StatusCreated
	if res.Booking.Status == state.StatusPendingPayment {
		status = http.StatusAccepted
	}
	return e.JSON(status, resp)
}

// ----- POST /bookings/{id}/cancel -----

type cancelReq struct {
	Reason string `json:"reason,omitempty"`
}

func (a *API) handleCancel(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	if !a.verifyTokenForBooking(e, id, token.ActionCancel) {
		return nil // error response already written
	}
	var req cancelReq
	_ = e.BindBody(&req)
	if _, err := a.Pipeline.Cancel(id, req.Reason, "invitee", e.RealIP()); err != nil {
		return internalErr(e)
	}
	return e.JSON(http.StatusOK, map[string]string{"status": "cancelled"})
}

// ----- POST /bookings/{id}/reschedule -----

type rescheduleReq struct {
	NewStartUTC time.Time `json:"new_start_utc"`
}

func (a *API) handleReschedule(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	if !a.verifyTokenForBooking(e, id, token.ActionReschedule) {
		return nil // error response already written
	}
	var req rescheduleReq
	if err := e.BindBody(&req); err != nil || req.NewStartUTC.IsZero() {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "new_start_utc required", nil)
	}
	b, tok, err := a.Pipeline.Reschedule(id, req.NewStartUTC.UTC(), "invitee", e.RealIP())
	if err != nil {
		return mapPipelineErr(e, err)
	}
	return e.JSON(http.StatusOK, map[string]any{
		"status":     string(b.Status),
		"booking_id": b.ID,
		"manage_url": manageURL(a.Pipeline, b.ID, tok.String),
		"start_utc":  b.StartUTC,
		"end_utc":    b.EndUTC,
	})
}

// ----- GET /bookings/{id} -----

func (a *API) handleGetBooking(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	if !a.verifyTokenForBooking(e, id, token.ActionView) {
		return nil // error response already written
	}
	b, err := a.Repo.FindBookingByID(id)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "booking not found", nil)
	}
	et, _ := a.Repo.FindEventTypeByID(b.EventTypeID)
	host, _ := a.Repo.FindHostByID(b.HostID)
	return e.JSON(http.StatusOK, map[string]any{
		"id":               b.ID,
		"status":           string(b.Status),
		"start_utc":        b.StartUTC,
		"end_utc":          b.EndUTC,
		"invitee_name":     b.InviteeName,
		"invitee_email":    b.InviteeEmail,
		"invitee_timezone": b.InviteeTimezone,
		"event_type": map[string]any{
			"title":         et.Title,
			"duration_min":  et.DurationMin,
			"location_type": et.LocationType,
		},
		"host": map[string]any{
			"name":     host.Name,
			"timezone": host.Timezone,
		},
		"meeting_url": b.MeetingJoinURL,
	})
}

// ----- helpers -----

// verifyTokenForBooking authorizes `action` on `bookingID` from the request's
// booking token. It returns false — with the error response ALREADY WRITTEN —
// when the request is not authorized; callers must `return nil` in that case.
//
// SECURITY (2026-07-16): this was previously `error`-returning and fed the
// result of writeErr straight through — but writeErr returns e.JSON's nil on
// success, so `if err != nil { return err }` guards NEVER fired and every
// handler kept executing after the 4xx was written. Net effect: ANY token
// (including garbage) could cancel/reschedule any known booking id. The bool
// contract makes the outcome explicit and impossible to ignore accidentally.
func (a *API) verifyTokenForBooking(e *core.RequestEvent, bookingID string, action token.Action) bool {
	tok := e.Request.Header.Get("X-Booking-Token")
	if tok == "" {
		tok = e.Request.URL.Query().Get("token")
	}
	if tok == "" {
		writeErr(e, httpStatusFor(CodeTokenInvalid), CodeTokenInvalid, "token required", nil)
		return false
	}
	b, err := a.Repo.FindBookingByID(bookingID)
	if err != nil {
		// Don't leak existence: same error code for missing booking.
		writeErr(e, httpStatusFor(CodeTokenInvalid), CodeTokenInvalid, "invalid token", nil)
		return false
	}
	tokenBookingID, tokenAction, err := a.Tokens.Verify(tok, b.CancelTokenHash)
	if err != nil {
		writeErr(e, httpStatusFor(mapTokenErr(err)), mapTokenErr(err), err.Error(), nil)
		return false
	}
	if tokenBookingID != bookingID {
		writeErr(e, httpStatusFor(CodeTokenInvalid), CodeTokenInvalid, "token-booking mismatch", nil)
		return false
	}
	if !tokenAuthorizes(tokenAction, action) {
		writeErr(e, httpStatusFor(CodeTokenInvalid), CodeTokenInvalid,
			"token does not authorize "+string(action), nil)
		return false
	}
	return true
}

// tokenAuthorizes decides whether a token minted for `granted` may perform
// `requested`. Policy (matches what the product actually issues and promises):
//   - The pipeline only ever mints ONE invitee token: the manage/view token
//     (confirmation mail: "verwalten (verschieben oder absagen)"). A view
//     token therefore carries full manage authority: view+cancel+reschedule.
//   - Action-specific tokens stay exclusive: a cancel token cannot
//     reschedule and vice versa (both may still view).
func tokenAuthorizes(granted, requested token.Action) bool {
	switch requested {
	case token.ActionView:
		return true // any non-revoked booking token authorizes view
	case token.ActionCancel:
		return granted == token.ActionCancel || granted == token.ActionView
	case token.ActionReschedule:
		return granted == token.ActionReschedule || granted == token.ActionView
	default:
		return false
	}
}

// hostPublicSlug is the host identifier we hand out in public URLs: the slug
// when the host has one, otherwise the email. Falling back to the email keeps
// clients that were built against the pre-slug API working unchanged.
func hostPublicSlug(h store.Host) string {
	if h.Slug != "" {
		return h.Slug
	}
	return h.Email
}

// findHostBySlug resolves the {host} segment of a public booking URL. It
// accepts, in order: the host's slug, their email, or their record ID.
//
// A slug can never contain '@' (see the users.slug pattern), so slugs and
// emails cannot collide — and skipping the slug lookup for anything that looks
// like an email saves a query on the still-common email links.
func (a *API) findHostBySlug(slugOrEmail string) (store.Host, error) {
	if !strings.Contains(slugOrEmail, "@") {
		if host, err := a.Repo.FindHostBySlug(slugOrEmail); err == nil {
			return host, nil
		}
	}
	host, err := a.Repo.FindHostByEmail(slugOrEmail)
	if err == nil {
		return host, nil
	}
	// Fallback: maybe the slug is actually an ID.
	return a.Repo.FindHostByID(slugOrEmail)
}

func mapPipelineErr(e *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, pipeline.ErrEventTypeInactive):
		return writeErr(e, httpStatusFor(CodeEventTypeInactive), CodeEventTypeInactive, "event_type inactive", nil)
	case errors.Is(err, pipeline.ErrSlotOutsideAvailability):
		return writeErr(e, httpStatusFor(CodeSlotOutsideAvail), CodeSlotOutsideAvail, "slot outside availability", nil)
	case errors.Is(err, pipeline.ErrSlotUnavailable):
		return writeErr(e, httpStatusFor(CodeSlotUnavailable), CodeSlotUnavailable, "slot already taken", nil)
	case errors.Is(err, pipeline.ErrCapacityFull):
		return writeErr(e, httpStatusFor(CodeSlotUnavailable), CodeSlotUnavailable, "slot capacity full", nil)
	case errors.Is(err, pipeline.ErrSlotTooSoon):
		return writeErr(e, httpStatusFor(CodeSlotTooSoon), CodeSlotTooSoon, "slot too soon", nil)
	case errors.Is(err, pipeline.ErrSlotTooFar):
		return writeErr(e, httpStatusFor(CodeSlotTooFar), CodeSlotTooFar, "slot beyond max_horizon", nil)
	case errors.Is(err, pipeline.ErrInvalidRequest):
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, err.Error(), nil)
	}
	if strings.Contains(err.Error(), "intake_validation_failed") {
		return writeErr(e, http.StatusBadRequest, CodeIntakeValidationFailed, err.Error(), nil)
	}
	if _, ok := err.(intake.Errors); ok {
		return writeErr(e, http.StatusBadRequest, CodeIntakeValidationFailed, err.Error(), nil)
	}
	return internalErr(e)
}

func mapTokenErr(err error) ErrorCode {
	switch {
	case errors.Is(err, token.ErrExpired):
		return CodeTokenExpired
	case errors.Is(err, token.ErrAlreadyUsed):
		return CodeTokenAlreadyUsed
	}
	return CodeTokenInvalid
}

func internalErr(e *core.RequestEvent) error {
	return writeErr(e, http.StatusInternalServerError, CodeInternalError, "internal error", nil)
}

// internalErrLogging variant — call with the actual err for log/diag.
func internalErrLog(e *core.RequestEvent, err error, ctx string) error {
	if err != nil {
		// Log to stderr so it shows in container logs; don't leak details to client.
		println("api/internal:", ctx, err.Error())
	}
	return internalErr(e)
}

// decodeJSONIfPresent returns the JSON-decoded value of raw bytes, or nil.
func decodeJSONIfPresent(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out any
	err := jsonDecode(raw, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// manageURL reaches into the pipeline to use its base-url; here we just
// rebuild the same shape.
func manageURL(p *pipeline.Pipeline, bookingID, tok string) string {
	// p.baseURL is unexported; we use a lightweight indirection.
	return p.ManageURL(bookingID, tok)
}

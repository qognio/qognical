// Package api — host-facing self-service routes mounted under /api/host/*.
//
// Auth: standard PocketBase user-collection bearer token (Authorization
// header). Records are scoped to the owner — every handler resolves the
// authenticated user record's ID and uses it as filter, so a host can never
// see or mutate another host's data.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"

	"github.com/qognio/qognical/internal/svctoken"
	"github.com/qognio/qognical/migrations"
)

// RegisterTeam mounts the read-only "team booking" endpoints for service
// consumers (e.g. an internal dashboard showing every host's upcoming
// appointments). Bearer-token auth via existing svctoken store; scope
// bookings:read is sufficient.
func (a *API) RegisterTeam(se *core.ServeEvent) {
	g := se.Router.Group("/api/team/v1")
	g.BindFunc(a.corsMiddleware)
	g.BindFunc(a.requireServiceTokenRead)

	g.GET("/bookings", a.teamBookings)
	g.GET("/hosts", a.teamHosts)
}

// svcTokenCtxKey is the request-context key under which requireServiceTokenRead
// stashes the resolved token so the team handlers can enforce its bindings.
const svcTokenCtxKey = "qognical.svctoken"

// requireServiceTokenRead validates the Authorization header against the
// svctoken store and ensures it carries the bookings:read scope. It stashes the
// resolved token in the request context so downstream handlers can enforce the
// token's host_binding / event_type_allowlist (SECURITY 2026-07-16: they were
// verified for scope but then discarded, so a host-bound token could read
// EVERY host's bookings + PII via /api/team/v1/*).
func (a *API) requireServiceTokenRead(e *core.RequestEvent) error {
	hdr := e.Request.Header.Get("Authorization")
	if hdr == "" {
		return writeErr(e, http.StatusUnauthorized, CodeTokenInvalid, "service token required", nil)
	}
	st, err := svctoken.Verify(a.Repo, hdr)
	if err != nil {
		return writeErr(e, http.StatusUnauthorized, CodeTokenInvalid, err.Error(), nil)
	}
	if !st.HasScope(svctoken.ScopeBookingsRead) {
		return writeErr(e, http.StatusForbidden, CodeTokenInvalid, "scope bookings:read required", nil)
	}
	e.Set(svcTokenCtxKey, st)
	return e.Next()
}

func (a *API) teamBookings(e *core.RequestEvent) error {
	q := e.Request.URL.Query()
	from := q.Get("from")
	to := q.Get("to")

	filter := "1 = 1"
	params := dbx.Params{}
	if from != "" {
		filter += " && start_utc >= {:from}"
		params["from"] = from
	}
	if to != "" {
		filter += " && start_utc < {:to}"
		params["to"] = to
	}
	// Enforce the token's scope — a host-scoped token must not read other
	// hosts' bookings, and an event-type-allowlisted token must not read
	// bookings of other event-types (2026-07-20: allowlist was ignored).
	if st, ok := e.Get(svcTokenCtxKey).(*svctoken.Resolved); ok {
		if st.Token.HostBinding != "" {
			filter += " && host = {:hb}"
			params["hb"] = st.Token.HostBinding
		}
		if len(st.Token.EventTypeAllowlist) > 0 {
			placeholders := make([]string, len(st.Token.EventTypeAllowlist))
			for i, id := range st.Token.EventTypeAllowlist {
				key := fmt.Sprintf("et%d", i)
				placeholders[i] = "{:" + key + "}"
				params[key] = id
			}
			filter += " && event_type IN (" + strings.Join(placeholders, ",") + ")"
		}
	}
	recs, err := e.App.FindRecordsByFilter(migrations.CollBookings, filter, "start_utc", 500, 0, params)
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		etID := r.GetString("event_type")
		etTitle := ""
		if et, err := e.App.FindRecordById(migrations.CollEventTypes, etID); err == nil {
			etTitle = et.GetString("title")
		}
		hostID := r.GetString("host")
		hostName, hostEmail := "", ""
		if hostID != "" {
			if h, err := e.App.FindRecordById(migrations.CollUsers, hostID); err == nil {
				hostName = h.GetString("name")
				hostEmail = h.Email()
			}
		}
		out = append(out, map[string]any{
			"id":               r.Id,
			"start_utc":        r.GetString("start_utc"),
			"end_utc":          r.GetString("end_utc"),
			"host_id":          hostID,
			"host_name":        hostName,
			"host_email":       hostEmail,
			"event_type_id":    etID,
			"event_type_title": etTitle,
			"status":           r.GetString("status"),
			// Guest details intentionally redacted — team-view shows that
			// somebody booked, not whom. Nexus dashboards are not the right
			// surface for PII; if a viewer needs the invitee details they
			// use the host dashboard with their own auth token.
		})
	}
	return e.JSON(http.StatusOK, out)
}

func (a *API) teamHosts(e *core.RequestEvent) error {
	filter := "role = 'host'"
	params := dbx.Params{}
	// A host-bound token sees only its own host, not the whole roster.
	if st, ok := e.Get(svcTokenCtxKey).(*svctoken.Resolved); ok && st.Token.HostBinding != "" {
		filter += " && id = {:hb}"
		params["hb"] = st.Token.HostBinding
	}
	recs, err := e.App.FindRecordsByFilter(migrations.CollUsers,
		filter, "name", 200, 0, params)
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, map[string]any{
			"id":       r.Id,
			"email":    r.Email(),
			"name":     r.GetString("name"),
			"timezone": r.GetString("timezone"),
		})
	}
	return e.JSON(http.StatusOK, out)
}

// RegisterHost attaches the self-service routes for hosts (qognical "users"
// collection). The same API struct is used for store/repo handle reuse.
func (a *API) RegisterHost(se *core.ServeEvent) {
	g := se.Router.Group("/api/host")
	g.BindFunc(a.corsMiddleware)
	g.Bind(apis.RequireAuth("users"))

	g.GET("/me", a.hostMe)
	g.GET("/stats", a.hostStats)
	g.GET("/bookings", a.hostBookings)

	g.GET("/event-types", a.hostListEventTypes)
	g.POST("/event-types", a.hostCreateEventType)
	g.PATCH("/event-types/{id}", a.hostUpdateEventType)
	g.DELETE("/event-types/{id}", a.hostDeleteEventType)

	g.GET("/availability", a.hostListAvailability)
	g.PUT("/availability", a.hostReplaceAvailability)

	g.GET("/date-overrides", a.hostListDateOverrides)
	g.POST("/date-overrides", a.hostCreateDateOverride)
	g.DELETE("/date-overrides/{id}", a.hostDeleteDateOverride)

	g.GET("/integrations", a.hostListIntegrations)
	g.POST("/integrations/msgraph", a.hostCreateMSGraphIntegration)
	g.DELETE("/integrations/{id}", a.hostDeleteIntegration)
}

// authUserID returns the authenticated user record id. Panic-safe because
// apis.RequireAuth("users") rejects unauthenticated requests upstream.
func (a *API) authUserID(e *core.RequestEvent) string {
	if e.Auth == nil {
		return ""
	}
	return e.Auth.Id
}

// requireHostRole blocks viewer/non-host users from write endpoints. Viewers
// can read team data but must not author event-types, change availability,
// or wire up integrations under their own name.
//
// Returns false with the error response ALREADY WRITTEN; callers must
// `return nil` then. (Was error-returning before, but writeErr returns
// e.JSON's nil → the `err != nil` guards never fired and viewers could
// write despite the 403 — same class of bug as verifyTokenForBooking,
// fixed 2026-07-16.)
func (a *API) requireHostRole(e *core.RequestEvent) bool {
	if e.Auth == nil {
		writeErr(e, http.StatusUnauthorized, CodeTokenInvalid, "no auth", nil)
		return false
	}
	if e.Auth.GetString("role") != "host" {
		writeErr(e, http.StatusForbidden, CodeTokenInvalid,
			"host role required for this action", nil)
		return false
	}
	return true
}

// --- /me

func (a *API) hostMe(e *core.RequestEvent) error {
	u := e.Auth
	if u == nil {
		return writeErr(e, http.StatusUnauthorized, CodeTokenInvalid, "no auth", nil)
	}
	return e.JSON(http.StatusOK, map[string]any{
		"id":             u.Id,
		"email":          u.Email(),
		"name":           u.GetString("name"),
		"slug":           u.GetString("slug"), // #16: dashboard builds public URLs from this (falls back to email)
		"timezone":       u.GetString("timezone"),
		"default_locale": u.GetString("default_locale"),
		"role":           u.GetString("role"),
		"verified":       u.GetBool("verified"),
	})
}

// --- /stats

func (a *API) hostStats(e *core.RequestEvent) error {
	uid := a.authUserID(e)
	role := ""
	if e.Auth != nil {
		role = e.Auth.GetString("role")
	}
	now := time.Now().UTC()

	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	endToday := startToday.Add(24 * time.Hour)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // Sun → 7 to compute Mon-anchored start
	}
	startWeek := startToday.AddDate(0, 0, -(weekday - 1))
	endWeek := startWeek.AddDate(0, 0, 7)
	startMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	endMonth := startMonth.AddDate(0, 1, 0)

	hostFilter := "host = {:uid}"
	countParams := func(from, to time.Time) dbx.Params {
		return dbx.Params{
			"uid":  uid,
			"from": from.Format(time.RFC3339),
			"to":   to.Format(time.RFC3339),
		}
	}
	if role == "viewer" {
		hostFilter = "1 = 1"
		countParams = func(from, to time.Time) dbx.Params {
			return dbx.Params{
				"from": from.Format(time.RFC3339),
				"to":   to.Format(time.RFC3339),
			}
		}
	}
	count := func(from, to time.Time) (int, error) {
		var n int
		err := e.App.DB().NewQuery(
			fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s AND start_utc >= {:from} AND start_utc < {:to} AND status NOT IN ('cancelled','declined')`,
				migrations.CollBookings, hostFilter)).
			Bind(countParams(from, to)).
			Row(&n)
		return n, err
	}

	today, _ := count(startToday, endToday)
	week, _ := count(startWeek, endWeek)
	month, _ := count(startMonth, endMonth)

	var activeETs int
	_ = e.App.DB().NewQuery(
		fmt.Sprintf(`SELECT count(*) FROM %s WHERE owner = {:uid} AND active = TRUE`, migrations.CollEventTypes)).
		Bind(dbx.Params{"uid": uid}).Row(&activeETs)

	integrations := []map[string]any{}
	recs, _ := e.App.FindRecordsByFilter(migrations.CollIntegrations,
		"owner = {:uid}", "-updated", 50, 0, dbx.Params{"uid": uid})
	for _, r := range recs {
		integrations = append(integrations, map[string]any{
			"provider":     r.GetString("provider"),
			"connected":    r.GetBool("sync_enabled"),
			"last_sync_at": r.GetString("last_sync_at"),
			"last_error":   r.GetString("last_error"),
		})
	}

	return e.JSON(http.StatusOK, map[string]any{
		"bookings_today":     today,
		"bookings_week":      week,
		"bookings_month":     month,
		"active_event_types": activeETs,
		"integrations":       integrations,
	})
}

// --- /bookings

func (a *API) hostBookings(e *core.RequestEvent) error {
	uid := a.authUserID(e)
	role := ""
	if e.Auth != nil {
		role = e.Auth.GetString("role")
	}
	q := e.Request.URL.Query()
	from := q.Get("from")
	to := q.Get("to")
	status := q.Get("status")

	// Viewers see every host's bookings. Hosts see only their own. This is the
	// minimum scaffolding for team-visibility — once we have per-team scoping
	// or owner-of-viewer relationships, narrow this filter accordingly.
	var filter string
	params := dbx.Params{}
	if role == "viewer" {
		filter = "1 = 1"
	} else {
		filter = "host = {:uid}"
		params["uid"] = uid
	}
	if from != "" {
		filter += " && start_utc >= {:from}"
		params["from"] = from
	}
	if to != "" {
		filter += " && start_utc < {:to}"
		params["to"] = to
	}
	if status != "" {
		filter += " && status = {:status}"
		params["status"] = status
	}

	recs, err := e.App.FindRecordsByFilter(migrations.CollBookings, filter, "start_utc", 200, 0, params)
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}

	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		etID := r.GetString("event_type")
		etTitle := ""
		if et, err := e.App.FindRecordById(migrations.CollEventTypes, etID); err == nil {
			etTitle = et.GetString("title")
		}
		hostID := r.GetString("host")
		hostName := ""
		if hostID != "" {
			if h, err := e.App.FindRecordById(migrations.CollUsers, hostID); err == nil {
				hostName = h.GetString("name")
			}
		}
		out = append(out, map[string]any{
			"id":               r.Id,
			"start_utc":        r.GetString("start_utc"),
			"end_utc":          r.GetString("end_utc"),
			"guest_name":       r.GetString("invitee_name"),
			"guest_email":      r.GetString("invitee_email"),
			"event_type_id":    etID,
			"event_type":       etID,
			"event_type_title": etTitle,
			"host_id":          hostID,
			"host_name":        hostName,
			"status":           r.GetString("status"),
			"meeting_url":      r.GetString("meeting_join_url"),
		})
	}
	return e.JSON(http.StatusOK, out)
}

// --- /event-types

func (a *API) hostListEventTypes(e *core.RequestEvent) error {
	uid := a.authUserID(e)
	recs, err := e.App.FindRecordsByFilter(migrations.CollEventTypes,
		"owner = {:uid}", "title", 200, 0, dbx.Params{"uid": uid})
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, eventTypeToMap(r))
	}
	return e.JSON(http.StatusOK, out)
}

type eventTypeInput struct {
	Title            string `json:"title"`
	Slug             string `json:"slug"`
	DurationMin      int    `json:"duration_min"`
	LocationType     string `json:"location_type"`
	MinNoticeMin     int    `json:"min_notice_min"`
	MaxHorizonDays   int    `json:"max_horizon_days"`
	BrandColor       string `json:"brand_color"`
	Active           *bool  `json:"active,omitempty"`
	RequiresApproval *bool  `json:"requires_approval,omitempty"`
	Capacity         int    `json:"capacity"`
}

func (a *API) hostCreateEventType(e *core.RequestEvent) error {
	if !a.requireHostRole(e) {
		return nil
	} // error response already written
	uid := a.authUserID(e)
	var in eventTypeInput
	if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "invalid json", nil)
	}
	if in.Title == "" || in.Slug == "" || in.DurationMin <= 0 {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "title, slug, duration_min required", nil)
	}
	if in.LocationType == "" {
		in.LocationType = "online_teams"
	}
	if in.MaxHorizonDays == 0 {
		in.MaxHorizonDays = 60
	}
	if in.BrandColor == "" {
		in.BrandColor = "#2563eb"
	}
	if in.Capacity == 0 {
		in.Capacity = 1
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	requiresApproval := false
	if in.RequiresApproval != nil {
		requiresApproval = *in.RequiresApproval
	}

	c, err := e.App.FindCollectionByNameOrId(migrations.CollEventTypes)
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	r := core.NewRecord(c)
	r.Set("owner", uid)
	r.Set("title", in.Title)
	r.Set("slug", in.Slug)
	r.Set("duration_min", in.DurationMin)
	r.Set("location_type", in.LocationType)
	r.Set("min_notice_min", in.MinNoticeMin)
	r.Set("max_horizon_days", in.MaxHorizonDays)
	r.Set("brand_color", in.BrandColor)
	r.Set("active", active)
	r.Set("requires_approval", requiresApproval)
	r.Set("capacity", in.Capacity)
	r.Set("assignment_strategy", "round_robin")
	r.Set("schema_version", 1)
	r.Set("locale", "de")
	r.Set("payment_mode", "none")
	r.Set("payment_currency", "EUR")
	r.Set("meeting_config", "{}")
	r.Set("intake_schema", "{}")
	r.Set("custom_css", "{}")
	r.Set("hosts", fmt.Sprintf(`["%s"]`, uid))

	if err := e.App.Save(r); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, err.Error(), nil)
	}
	return e.JSON(http.StatusCreated, eventTypeToMap(r))
}

func (a *API) hostUpdateEventType(e *core.RequestEvent) error {
	if !a.requireHostRole(e) {
		return nil
	} // error response already written
	uid := a.authUserID(e)
	id := e.Request.PathValue("id")
	r, err := e.App.FindRecordById(migrations.CollEventTypes, id)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "event_type not found", nil)
	}
	if r.GetString("owner") != uid {
		return writeErr(e, http.StatusForbidden, CodeInvalidRequest, "not your event_type", nil)
	}
	var in map[string]any
	if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "invalid json", nil)
	}
	allowed := map[string]bool{
		"title": true, "slug": true, "duration_min": true,
		"location_type": true, "min_notice_min": true, "max_horizon_days": true,
		"brand_color": true, "active": true, "requires_approval": true,
		"capacity": true, "description": true,
	}
	for k, v := range in {
		if allowed[k] {
			r.Set(k, v)
		}
	}
	if err := e.App.Save(r); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, err.Error(), nil)
	}
	return e.JSON(http.StatusOK, eventTypeToMap(r))
}

func (a *API) hostDeleteEventType(e *core.RequestEvent) error {
	if !a.requireHostRole(e) {
		return nil
	} // error response already written
	uid := a.authUserID(e)
	id := e.Request.PathValue("id")
	r, err := e.App.FindRecordById(migrations.CollEventTypes, id)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "event_type not found", nil)
	}
	if r.GetString("owner") != uid {
		return writeErr(e, http.StatusForbidden, CodeInvalidRequest, "not your event_type", nil)
	}
	if err := e.App.Delete(r); err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	return e.NoContent(http.StatusNoContent)
}

func eventTypeToMap(r *core.Record) map[string]any {
	return map[string]any{
		"id":                r.Id,
		"owner":             r.GetString("owner"),
		"title":             r.GetString("title"),
		"slug":              r.GetString("slug"),
		"duration_min":      r.GetInt("duration_min"),
		"location_type":     r.GetString("location_type"),
		"min_notice_min":    r.GetInt("min_notice_min"),
		"max_horizon_days":  r.GetInt("max_horizon_days"),
		"brand_color":       r.GetString("brand_color"),
		"active":            r.GetBool("active"),
		"requires_approval": r.GetBool("requires_approval"),
		"capacity":          r.GetInt("capacity"),
		"description":       r.GetString("description"),
	}
}

// --- /availability

func (a *API) hostListAvailability(e *core.RequestEvent) error {
	uid := a.authUserID(e)
	recs, err := e.App.FindRecordsByFilter(migrations.CollAvailability,
		"owner = {:uid}", "weekday,start", 200, 0, dbx.Params{"uid": uid})
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, map[string]any{
			"id":      r.Id,
			"weekday": r.GetInt("weekday"),
			"start":   r.GetString("start"),
			"end":     r.GetString("end"),
		})
	}
	return e.JSON(http.StatusOK, out)
}

func (a *API) hostReplaceAvailability(e *core.RequestEvent) error {
	if !a.requireHostRole(e) {
		return nil
	} // error response already written
	uid := a.authUserID(e)
	var in []struct {
		Weekday int    `json:"weekday"`
		Start   string `json:"start"`
		End     string `json:"end"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "invalid json", nil)
	}
	for _, row := range in {
		if row.Weekday < 0 || row.Weekday > 6 {
			return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "weekday must be 0..6 (0=Mon)", nil)
		}
		if !validTime(row.Start) || !validTime(row.End) {
			return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "start/end must be HH:MM", nil)
		}
		if row.Start >= row.End {
			return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "end must be after start", nil)
		}
	}

	c, err := e.App.FindCollectionByNameOrId(migrations.CollAvailability)
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}

	txErr := e.App.RunInTransaction(func(tx core.App) error {
		old, err := tx.FindRecordsByFilter(migrations.CollAvailability,
			"owner = {:uid}", "", 1000, 0, dbx.Params{"uid": uid})
		if err != nil {
			return err
		}
		for _, r := range old {
			if err := tx.Delete(r); err != nil {
				return err
			}
		}
		for _, row := range in {
			r := core.NewRecord(c)
			r.Set("owner", uid)
			r.Set("weekday", row.Weekday)
			r.Set("start", row.Start)
			r.Set("end", row.End)
			if err := tx.Save(r); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, txErr.Error(), nil)
	}
	return a.hostListAvailability(e)
}

func validTime(t string) bool {
	if len(t) != 5 || t[2] != ':' {
		return false
	}
	for i, c := range t {
		if i == 2 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// --- /date-overrides

func (a *API) hostListDateOverrides(e *core.RequestEvent) error {
	uid := a.authUserID(e)
	recs, err := e.App.FindRecordsByFilter(migrations.CollDateOverrides,
		"owner = {:uid}", "date", 365, 0, dbx.Params{"uid": uid})
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		// Translate the schema vocabulary back to the API/dashboard one.
		apiType := "closed"
		if r.GetString("type") == "custom_hours" {
			apiType = "open"
		}
		out = append(out, map[string]any{
			"id":    r.Id,
			"date":  r.GetString("date"),
			"type":  apiType,
			"start": r.GetString("start"),
			"end":   r.GetString("end"),
		})
	}
	return e.JSON(http.StatusOK, out)
}

func (a *API) hostCreateDateOverride(e *core.RequestEvent) error {
	if !a.requireHostRole(e) {
		return nil
	} // error response already written
	uid := a.authUserID(e)
	var in struct {
		Date  string `json:"date"`
		Type  string `json:"type"`
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "invalid json", nil)
	}
	if in.Date == "" || (in.Type != "closed" && in.Type != "open") {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "date required, type=closed|open", nil)
	}
	// The API/dashboard vocabulary is closed|open, but the collection's SelectField
	// only accepts unavailable|custom_hours — so every override Save() was rejected
	// by schema validation (2026-07-20). Translate at the boundary.
	schemaType := "unavailable"
	if in.Type == "open" {
		schemaType = "custom_hours"
	}
	c, err := e.App.FindCollectionByNameOrId(migrations.CollDateOverrides)
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	r := core.NewRecord(c)
	r.Set("owner", uid)
	r.Set("date", in.Date)
	r.Set("type", schemaType)
	if in.Type == "open" {
		r.Set("start", in.Start)
		r.Set("end", in.End)
	}
	if err := e.App.Save(r); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, err.Error(), nil)
	}
	return e.JSON(http.StatusCreated, map[string]any{"id": r.Id})
}

func (a *API) hostDeleteDateOverride(e *core.RequestEvent) error {
	if !a.requireHostRole(e) {
		return nil
	} // error response already written
	uid := a.authUserID(e)
	id := e.Request.PathValue("id")
	r, err := e.App.FindRecordById(migrations.CollDateOverrides, id)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "not found", nil)
	}
	if r.GetString("owner") != uid {
		return writeErr(e, http.StatusForbidden, CodeInvalidRequest, "not yours", nil)
	}
	if err := e.App.Delete(r); err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	return e.NoContent(http.StatusNoContent)
}

// --- /integrations

func (a *API) hostListIntegrations(e *core.RequestEvent) error {
	uid := a.authUserID(e)
	recs, err := e.App.FindRecordsByFilter(migrations.CollIntegrations,
		"owner = {:uid}", "provider", 50, 0, dbx.Params{"uid": uid})
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, map[string]any{
			"id":           r.Id,
			"provider":     r.GetString("provider"),
			"sync_enabled": r.GetBool("sync_enabled"),
			"last_sync_at": r.GetString("last_sync_at"),
			"last_error":   r.GetString("last_error"),
		})
	}
	return e.JSON(http.StatusOK, out)
}

func (a *API) hostCreateMSGraphIntegration(e *core.RequestEvent) error {
	if !a.requireHostRole(e) {
		return nil
	} // error response already written
	uid := a.authUserID(e)
	var in struct {
		TenantID     string `json:"tenant_id"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		UserID       string `json:"user_id"`
	}
	if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "invalid json", nil)
	}
	if in.TenantID == "" || in.ClientID == "" || in.ClientSecret == "" || in.UserID == "" {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest,
			"tenant_id, client_id, client_secret, user_id required", nil)
	}
	credsJSON, err := json.Marshal(in)
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	// Encrypt at rest — the adapter registry DECRYPTS credentials on load, so
	// plaintext rows fail to decrypt and every meeting/calendar call dies
	// silently (found 2026-07-16: host-API-created integrations never worked;
	// only the CLI path encrypted). Same crypto as `qognical integrations set`.
	if a.Master == nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError,
			"encryption key not configured", nil)
	}
	ciphertext, err := a.Master.Encrypt(credsJSON)
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}

	c, err := e.App.FindCollectionByNameOrId(migrations.CollIntegrations)
	if err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}

	existing, _ := e.App.FindFirstRecordByFilter(migrations.CollIntegrations,
		"owner = {:uid} && provider = 'msgraph'", dbx.Params{"uid": uid})
	var r *core.Record
	if existing != nil {
		r = existing
	} else {
		r = core.NewRecord(c)
		r.Set("owner", uid)
		r.Set("provider", "msgraph")
	}
	r.Set("credentials", ciphertext)
	r.Set("config", "{}")
	r.Set("sync_enabled", true)
	r.Set("last_error", "")
	if err := e.App.Save(r); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, err.Error(), nil)
	}
	return e.JSON(http.StatusCreated, map[string]any{
		"id":           r.Id,
		"provider":     "msgraph",
		"sync_enabled": true,
	})
}

func (a *API) hostDeleteIntegration(e *core.RequestEvent) error {
	if !a.requireHostRole(e) {
		return nil
	} // error response already written
	uid := a.authUserID(e)
	id := e.Request.PathValue("id")
	r, err := e.App.FindRecordById(migrations.CollIntegrations, id)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "not found", nil)
	}
	if r.GetString("owner") != uid {
		return writeErr(e, http.StatusForbidden, CodeInvalidRequest, "not yours", nil)
	}
	if err := e.App.Delete(r); err != nil {
		return writeErr(e, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
	}
	return e.NoContent(http.StatusNoContent)
}

// avoid unused import lint when we trim handlers later.
var _ = errors.New
var _ = strings.TrimSpace

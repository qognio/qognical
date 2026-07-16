// Package google implements adapters.CalendarProvider for Google Calendar
// (ADR-0001). Auth is OAuth2 with offline access; we store the refresh_token
// in encrypted credentials and exchange it for a short-lived access_token
// per request batch.
//
// Endpoints:
//
//	POST https://oauth2.googleapis.com/token            (refresh)
//	POST https://www.googleapis.com/calendar/v3/freeBusy
//	POST /calendar/v3/calendars/{calId}/events
//	PATCH /calendar/v3/calendars/{calId}/events/{evtId}
//	DELETE /calendar/v3/calendars/{calId}/events/{evtId}
//
// We do NOT pull in google.golang.org/api — stdlib HTTP is plenty.
package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/adapters/httpx"
	"github.com/qognio/qognical/internal/timeutil"
)

const Name = "google"

// Credentials JSON (encrypted at rest). Two auth modes are supported:
//
//   - OAuth2 user flow: client_id + client_secret + refresh_token. CalendarID
//     defaults to "primary" (the consenting user's own calendar).
//   - Service-account flow: a Google SA key JSON (type=="service_account" with
//     private_key/client_email/token_uri). The adapter signs a JWT and uses the
//     jwt-bearer grant. There is no user consent; instead the target calendar is
//     shared with the SA's client_email. CalendarID MUST be the shared calendar's
//     address (e.g. the owner's gmail), because "primary" would resolve to the
//     SA's own (empty) calendar.
type Credentials struct {
	// OAuth2 user flow
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// Service-account flow (fields mirror a Google SA key JSON)
	Type         string `json:"type,omitempty"`
	PrivateKey   string `json:"private_key,omitempty"`
	PrivateKeyID string `json:"private_key_id,omitempty"`
	ClientEmail  string `json:"client_email,omitempty"`
	TokenURI     string `json:"token_uri,omitempty"`
	// common
	CalendarID string `json:"calendar_id"` // "primary" or specific id/address
}

func (c Credentials) isServiceAccount() bool { return c.Type == "service_account" }

type Config struct {
	OAuthBase string `json:"oauth_base,omitempty"`
	APIBase   string `json:"api_base,omitempty"`
}

func Factory(credsRaw, confRaw json.RawMessage) (adapters.CalendarProvider, error) {
	var c Credentials
	if err := json.Unmarshal(credsRaw, &c); err != nil {
		return nil, fmt.Errorf("google creds: %w", err)
	}
	if c.isServiceAccount() {
		if c.PrivateKey == "" || c.ClientEmail == "" {
			return nil, errors.New("google: service_account requires private_key and client_email")
		}
		if c.TokenURI == "" {
			c.TokenURI = "https://oauth2.googleapis.com/token"
		}
		if c.CalendarID == "" {
			return nil, errors.New("google: service_account requires calendar_id (the shared calendar address; 'primary' would be the SA's own calendar)")
		}
	} else {
		if c.ClientID == "" || c.ClientSecret == "" || c.RefreshToken == "" {
			return nil, errors.New("google: client_id/client_secret/refresh_token required")
		}
		if c.CalendarID == "" {
			c.CalendarID = "primary"
		}
	}
	cfg := Config{
		OAuthBase: "https://oauth2.googleapis.com",
		APIBase:   "https://www.googleapis.com",
	}
	if len(confRaw) > 0 {
		_ = json.Unmarshal(confRaw, &cfg)
	}
	return &Provider{creds: c, cfg: cfg}, nil
}

type Provider struct {
	creds Credentials
	cfg   Config

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

func (p *Provider) Name() string { return Name }

// ----- auth -----

func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.tokenExp.Add(-60*time.Second)) {
		return p.token, nil
	}
	var form map[string]string
	if p.creds.isServiceAccount() {
		assertion, err := p.signJWT(time.Now())
		if err != nil {
			return "", fmt.Errorf("%w: sign jwt: %v", adapters.ErrAuth, err)
		}
		form = map[string]string{
			"grant_type": "urn:ietf:params:oauth:grant-type:jwt-bearer",
			"assertion":  assertion,
		}
	} else {
		form = map[string]string{
			"client_id":     p.creds.ClientID,
			"client_secret": p.creds.ClientSecret,
			"refresh_token": p.creds.RefreshToken,
			"grant_type":    "refresh_token",
		}
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	err := httpx.DoForm(ctx, p.cfg.OAuthBase+"/token", form, &tr)
	if err != nil {
		return "", err
	}
	if tr.Error == "invalid_grant" || tr.AccessToken == "" {
		// Refresh-token revocation surfaces here. Map to ErrAuth so the
		// caller updates last_error and tells the host to re-connect.
		return "", fmt.Errorf("%w: %s", adapters.ErrAuth, tr.Error)
	}
	p.token = tr.AccessToken
	p.tokenExp = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return p.token, nil
}

func (p *Provider) headers(ctx context.Context) (map[string]string, error) {
	t, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + t}, nil
}

// ----- CalendarProvider -----

func (p *Provider) FreeBusy(ctx context.Context, from, to time.Time) ([]timeutil.Interval, error) {
	headers, err := p.headers(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"timeMin":  from.UTC().Format(time.RFC3339),
		"timeMax":  to.UTC().Format(time.RFC3339),
		"timeZone": "UTC",
		"items":    []map[string]any{{"id": p.creds.CalendarID}},
	}
	var resp struct {
		Calendars map[string]struct {
			Busy []struct {
				Start time.Time `json:"start"`
				End   time.Time `json:"end"`
			} `json:"busy"`
		} `json:"calendars"`
	}
	if err := httpx.DoJSON(ctx, "POST", p.cfg.APIBase+"/calendar/v3/freeBusy", headers, body, &resp); err != nil {
		return nil, err
	}
	cal, ok := resp.Calendars[p.creds.CalendarID]
	if !ok {
		return nil, nil
	}
	out := make([]timeutil.Interval, 0, len(cal.Busy))
	for _, b := range cal.Busy {
		out = append(out, timeutil.Interval{Start: b.Start.UTC(), End: b.End.UTC()})
	}
	return out, nil
}

func (p *Provider) CreateEvent(ctx context.Context, in adapters.CalendarEvent) (adapters.CreatedEvent, error) {
	headers, err := p.headers(ctx)
	if err != nil {
		return adapters.CreatedEvent{}, err
	}
	body := map[string]any{
		"summary":     in.Summary,
		"description": in.Description,
		"location":    in.Location,
		"start":       map[string]any{"dateTime": in.StartUTC.Format(time.RFC3339), "timeZone": "UTC"},
		"end":         map[string]any{"dateTime": in.EndUTC.Format(time.RFC3339), "timeZone": "UTC"},
	}
	// A service account cannot invite attendees without Domain-Wide Delegation
	// (Google returns 403 forbiddenForServiceAccounts). In SA mode we create the
	// event on the owner's shared calendar WITHOUT attendees; the invitee is
	// notified via qognical's own confirmation mail (incl. meeting link), not a
	// Google calendar invite. The OAuth user flow keeps inviting attendees.
	if !p.creds.isServiceAccount() {
		body["attendees"] = []map[string]any{
			{"email": in.AttendeeMail, "displayName": in.AttendeeName, "responseStatus": "needsAction"},
		}
	}
	// v0.3 Google Meet: when the caller asks for an online meeting, attach a
	// conferenceData.createRequest block. Google returns the join URL in
	// event.conferenceData.entryPoints[type=video].uri after creation.
	//
	// Meet creation only works in the OAuth user flow. A service account on a
	// shared consumer calendar gets a 400 "Invalid conference type value"
	// (Meet needs a real user / Workspace context), so we skip conferenceData
	// in SA mode — the event is still created, just without an auto Meet link.
	if in.CreateOnlineMeeting && !p.creds.isServiceAccount() {
		body["conferenceData"] = map[string]any{
			"createRequest": map[string]any{
				"requestId":             fmt.Sprintf("qog-%d", in.StartUTC.UnixNano()),
				"conferenceSolutionKey": map[string]any{"type": "hangoutsMeet"},
			},
		}
	}
	type entryPoint struct {
		EntryPointType string `json:"entryPointType"`
		URI            string `json:"uri"`
	}
	var resp struct {
		ID             string `json:"id"`
		ConferenceData struct {
			ConferenceID string       `json:"conferenceId"`
			EntryPoints  []entryPoint `json:"entryPoints"`
		} `json:"conferenceData"`
		HangoutLink string `json:"hangoutLink"`
	}
	url := fmt.Sprintf("%s/calendar/v3/calendars/%s/events?conferenceDataVersion=1",
		p.cfg.APIBase, p.creds.CalendarID)
	if err := httpx.DoJSON(ctx, "POST", url, headers, body, &resp); err != nil {
		return adapters.CreatedEvent{}, err
	}
	out := adapters.CreatedEvent{ExternalID: resp.ID, MeetingID: resp.ConferenceData.ConferenceID}
	// hangoutLink is populated for Meet conferences; the entryPoints list is
	// authoritative for newer events.
	if resp.HangoutLink != "" {
		out.MeetingURL = resp.HangoutLink
	}
	for _, ep := range resp.ConferenceData.EntryPoints {
		if ep.EntryPointType == "video" && ep.URI != "" {
			out.MeetingURL = ep.URI
			break
		}
	}
	return out, nil
}

func (p *Provider) UpdateEvent(ctx context.Context, externalID string, in adapters.CalendarEvent) error {
	headers, err := p.headers(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"summary": in.Summary,
		"start":   map[string]any{"dateTime": in.StartUTC.Format(time.RFC3339), "timeZone": "UTC"},
		"end":     map[string]any{"dateTime": in.EndUTC.Format(time.RFC3339), "timeZone": "UTC"},
	}
	url := fmt.Sprintf("%s/calendar/v3/calendars/%s/events/%s", p.cfg.APIBase, p.creds.CalendarID, externalID)
	return httpx.DoJSON(ctx, "PATCH", url, headers, body, nil)
}

func (p *Provider) DeleteEvent(ctx context.Context, externalID string) error {
	headers, err := p.headers(ctx)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/calendar/v3/calendars/%s/events/%s", p.cfg.APIBase, p.creds.CalendarID, externalID)
	err = httpx.DoJSON(ctx, "DELETE", url, headers, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

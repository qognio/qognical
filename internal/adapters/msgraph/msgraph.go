// Package msgraph implements adapters.CalendarProvider against Microsoft
// Graph using the application-permissions (client-credentials) flow.
// Designed for the small-team Use-Case from docs/planning/05: one app
// registration per tenant, an Application Access Policy locking the
// service principal to specific mailboxes.
//
// Teams meetings are created in the same POST that creates the calendar
// event (isOnlineMeeting=true, onlineMeetingProvider=teamsForBusiness) —
// the Doc-05 "Weg 1".
//
// Endpoints we call:
//
//	POST https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token   (auth)
//	POST https://graph.microsoft.com/v1.0/users/{userId}/calendar/getSchedule
//	POST https://graph.microsoft.com/v1.0/users/{userId}/events
//	PATCH /v1.0/users/{userId}/events/{eventId}
//	DELETE /v1.0/users/{userId}/events/{eventId}
//
// The token is cached in memory until ~60s before expiry.
package msgraph

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

const Name = "msgraph"

// Credentials is the JSON shape stored encrypted in integrations.credentials.
type Credentials struct {
	TenantID     string `json:"tenant_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// UserID is the immutable user id (objectId or UPN) of the mailbox we
	// operate on. Application Access Policy must whitelist this user.
	UserID string `json:"user_id"`
}

// Config is unencrypted runtime settings (mostly endpoint overrides for tests).
type Config struct {
	GraphBase string `json:"graph_base,omitempty"`
	LoginBase string `json:"login_base,omitempty"`
}

// Factory satisfies adapters.CalendarFactory.
func Factory(credsRaw, confRaw json.RawMessage) (adapters.CalendarProvider, error) {
	var c Credentials
	if err := json.Unmarshal(credsRaw, &c); err != nil {
		return nil, fmt.Errorf("msgraph creds: %w", err)
	}
	if c.TenantID == "" || c.ClientID == "" || c.ClientSecret == "" || c.UserID == "" {
		return nil, errors.New("msgraph: tenant_id/client_id/client_secret/user_id required")
	}
	cfg := Config{
		GraphBase: "https://graph.microsoft.com",
		LoginBase: "https://login.microsoftonline.com",
	}
	if len(confRaw) > 0 {
		_ = json.Unmarshal(confRaw, &cfg)
	}
	return &Provider{creds: c, cfg: cfg}, nil
}

// Provider is the runtime instance.
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
	type tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	var tr tokenResp
	err := httpx.DoForm(ctx,
		fmt.Sprintf("%s/%s/oauth2/v2.0/token", p.cfg.LoginBase, p.creds.TenantID),
		map[string]string{
			"client_id":     p.creds.ClientID,
			"client_secret": p.creds.ClientSecret,
			"scope":         "https://graph.microsoft.com/.default",
			"grant_type":    "client_credentials",
		}, &tr)
	if err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", errors.New("msgraph: empty access token")
	}
	p.token = tr.AccessToken
	p.tokenExp = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return p.token, nil
}

func (p *Provider) authHeaders(ctx context.Context) (map[string]string, error) {
	tok, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + tok}, nil
}

// ----- CalendarProvider -----

func (p *Provider) FreeBusy(ctx context.Context, from, to time.Time) ([]timeutil.Interval, error) {
	headers, err := p.authHeaders(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"schedules":                []string{p.creds.UserID},
		"startTime":                graphTimeRange(from),
		"endTime":                  graphTimeRange(to),
		"availabilityViewInterval": 15,
	}
	type item struct {
		Status string `json:"status"`
		Start  struct {
			DateTime string `json:"dateTime"`
		} `json:"start"`
		End struct {
			DateTime string `json:"dateTime"`
		} `json:"end"`
	}
	var resp struct {
		Value []struct {
			ScheduleItems []item `json:"scheduleItems"`
		} `json:"value"`
	}
	url := fmt.Sprintf("%s/v1.0/users/%s/calendar/getSchedule", p.cfg.GraphBase, p.creds.UserID)
	if err := httpx.DoJSON(ctx, "POST", url, headers, body, &resp); err != nil {
		return nil, err
	}
	out := []timeutil.Interval{}
	for _, schedule := range resp.Value {
		for _, it := range schedule.ScheduleItems {
			if it.Status == "free" || it.Status == "" {
				continue
			}
			s, errS := parseGraphTime(it.Start.DateTime)
			e, errE := parseGraphTime(it.End.DateTime)
			if errS != nil || errE != nil {
				continue
			}
			out = append(out, timeutil.Interval{Start: s, End: e})
		}
	}
	return out, nil
}

func (p *Provider) CreateEvent(ctx context.Context, in adapters.CalendarEvent) (adapters.CreatedEvent, error) {
	headers, err := p.authHeaders(ctx)
	if err != nil {
		return adapters.CreatedEvent{}, err
	}
	body := map[string]any{
		"subject":  in.Summary,
		"body":     map[string]any{"contentType": "HTML", "content": in.Description},
		"start":    map[string]any{"dateTime": in.StartUTC.Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
		"end":      map[string]any{"dateTime": in.EndUTC.Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
		"location": map[string]any{"displayName": in.Location},
		"attendees": []map[string]any{
			{
				"emailAddress": map[string]any{"address": in.AttendeeMail, "name": in.AttendeeName},
				"type":         "required",
			},
		},
	}
	if in.CreateOnlineMeeting {
		body["isOnlineMeeting"] = true
		body["onlineMeetingProvider"] = "teamsForBusiness"
	}
	var resp struct {
		ID            string `json:"id"`
		OnlineMeeting struct {
			JoinURL string `json:"joinUrl"`
		} `json:"onlineMeeting"`
	}
	url := fmt.Sprintf("%s/v1.0/users/%s/events", p.cfg.GraphBase, p.creds.UserID)
	if err := httpx.DoJSON(ctx, "POST", url, headers, body, &resp); err != nil {
		return adapters.CreatedEvent{}, err
	}
	return adapters.CreatedEvent{
		ExternalID: resp.ID,
		MeetingURL: resp.OnlineMeeting.JoinURL,
	}, nil
}

func (p *Provider) UpdateEvent(ctx context.Context, externalID string, in adapters.CalendarEvent) error {
	headers, err := p.authHeaders(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"subject": in.Summary,
		"start":   map[string]any{"dateTime": in.StartUTC.Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
		"end":     map[string]any{"dateTime": in.EndUTC.Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
	}
	url := fmt.Sprintf("%s/v1.0/users/%s/events/%s", p.cfg.GraphBase, p.creds.UserID, externalID)
	return httpx.DoJSON(ctx, "PATCH", url, headers, body, nil)
}

func (p *Provider) DeleteEvent(ctx context.Context, externalID string) error {
	headers, err := p.authHeaders(ctx)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/v1.0/users/%s/events/%s", p.cfg.GraphBase, p.creds.UserID, externalID)
	err = httpx.DoJSON(ctx, "DELETE", url, headers, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// graphTimeRange returns the {dateTime, timeZone} pair Graph wants on
// getSchedule. We always pass UTC.
func graphTimeRange(t time.Time) map[string]any {
	return map[string]any{
		"dateTime": t.UTC().Format("2006-01-02T15:04:05"),
		"timeZone": "UTC",
	}
}

// parseGraphTime accepts the timestamps Graph returns ("2026-06-01T07:00:00.0000000").
func parseGraphTime(s string) (time.Time, error) {
	// Try the most-precise layout first.
	for _, layout := range []string{
		"2006-01-02T15:04:05.9999999",
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparsable time %q", s)
}

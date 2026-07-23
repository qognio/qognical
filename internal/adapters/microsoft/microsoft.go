// Package microsoft implements adapters.CalendarProvider for Microsoft 365 /
// Outlook calendars using the DELEGATED OAuth2 authorization-code flow
// (offline_access + Calendars.ReadWrite). It mirrors the Google OAuth user
// flow in google.go: a refresh_token held in encrypted credentials is
// exchanged for a short-lived access_token per request batch, and every call
// targets the consenting user's own mailbox via /me.
//
// This is deliberately DISTINCT from the sibling "msgraph" adapter, which uses
// the application-permissions (client-credentials) flow and needs a tenant
// Application Access Policy. The delegated flow needs no tenant-admin policy:
// the user (e.g. Ralf Stork) consents once and we hold their refresh_token —
// exactly the shape of the bot-manager MS-Graph connector already in prod
// (services/msgraph-sync.ts).
//
// Endpoints:
//
//	POST   https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token  (refresh)
//	GET    https://graph.microsoft.com/v1.0/me/calendarView             (busy)
//	POST   /v1.0/me/events                                               (create)
//	PATCH  /v1.0/me/events/{id}                                          (update)
//	DELETE /v1.0/me/events/{id}                                          (delete)
//
// No Graph SDK — stdlib HTTP via the shared httpx helper, exactly like google.go.
package microsoft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/adapters/httpx"
	"github.com/qognio/qognical/internal/timeutil"
)

const Name = "microsoft"

// maxPages caps calendarView pagination. At $top=200 that is 10 000 events per
// FreeBusy window — far beyond any realistic booking horizon, but bounded so a
// pathological mailbox can never spin the loop forever.
const maxPages = 50

// Credentials is the JSON shape stored encrypted in integrations.credentials.
// It mirrors the Google OAuth user flow (client_id/client_secret/refresh_token)
// rather than the app-permissions msgraph adapter (tenant/user_id/secret).
type Credentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	// Tenant selects the token authority: "common" (any account), "organizations"
	// (any work/school account), "consumers", or a specific tenant GUID/domain.
	// Defaults to "common".
	Tenant string `json:"tenant,omitempty"`
	// CalendarID optionally targets a non-default calendar of the signed-in user.
	// Empty → the user's default calendar (/me/events, /me/calendarView).
	CalendarID string `json:"calendar_id,omitempty"`
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
		return nil, fmt.Errorf("microsoft creds: %w", err)
	}
	if c.ClientID == "" || c.ClientSecret == "" || c.RefreshToken == "" {
		return nil, errors.New("microsoft: client_id/client_secret/refresh_token required")
	}
	if c.Tenant == "" {
		c.Tenant = "common"
	}
	cfg := Config{
		GraphBase: "https://graph.microsoft.com",
		LoginBase: "https://login.microsoftonline.com",
	}
	if len(confRaw) > 0 {
		_ = json.Unmarshal(confRaw, &cfg)
	}
	return &Provider{creds: c, cfg: cfg, refreshTok: c.RefreshToken}, nil
}

// Provider is the runtime instance.
type Provider struct {
	creds Credentials
	cfg   Config

	mu       sync.Mutex
	token    string
	tokenExp time.Time
	// refreshTok holds the CURRENT refresh_token. MS rotates it on every
	// refresh (sliding ~90-day window); we keep the newest here so a long-lived
	// process stays authorised, and — when onCredsChange is wired by the
	// registry (adapters.CredentialRotator) — persist it re-encrypted so the
	// rotation also survives restarts and freshly-built provider instances.
	refreshTok string
	// onCredsChange, if set, is called with the full updated credentials JSON
	// whenever the refresh_token rotates. Best-effort persistence.
	onCredsChange func(json.RawMessage)
}

func (p *Provider) Name() string { return Name }

// SetOnCredentialChange implements adapters.CredentialRotator.
func (p *Provider) SetOnCredentialChange(fn func(json.RawMessage)) {
	p.mu.Lock()
	p.onCredsChange = fn
	p.mu.Unlock()
}

// ----- auth -----

func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.tokenExp.Add(-60*time.Second)) {
		return p.token, nil
	}
	form := map[string]string{
		"client_id":     p.creds.ClientID,
		"client_secret": p.creds.ClientSecret,
		"refresh_token": p.refreshTok,
		"grant_type":    "refresh_token",
		"scope":         "offline_access https://graph.microsoft.com/Calendars.ReadWrite",
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	tokenURL := fmt.Sprintf("%s/%s/oauth2/v2.0/token", p.cfg.LoginBase, p.creds.Tenant)
	if err := httpx.DoForm(ctx, tokenURL, form, &tr); err != nil {
		// invalid_grant surfaces as an HTTP 400 with error=invalid_grant in the
		// body (refresh token expired/revoked, password changed). httpx returns
		// that as an APIError; map it to ErrAuth so the pipeline flags the host
		// for re-connect instead of retrying forever.
		if isInvalidGrant(err) {
			return "", fmt.Errorf("%w: refresh token rejected (re-connect required)", adapters.ErrAuth)
		}
		return "", err
	}
	if tr.Error == "invalid_grant" || tr.AccessToken == "" {
		return "", fmt.Errorf("%w: %s", adapters.ErrAuth, tr.Error)
	}
	p.token = tr.AccessToken
	p.tokenExp = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	// MS rotates the refresh_token on each refresh. Keep the newest and, if a
	// persist hook is wired, hand back the full updated credentials so they are
	// stored re-encrypted; otherwise the rotation is lost on the next
	// registry-built instance / restart and the integration expires early.
	if tr.RefreshToken != "" && tr.RefreshToken != p.refreshTok {
		p.refreshTok = tr.RefreshToken
		if p.onCredsChange != nil {
			updated := p.creds
			updated.RefreshToken = tr.RefreshToken
			if b, err := json.Marshal(updated); err == nil {
				p.onCredsChange(b)
			}
		}
	}
	return p.token, nil
}

func (p *Provider) headers(ctx context.Context) (map[string]string, error) {
	t, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + t}, nil
}

// calendarPath returns the Graph path prefix for the target calendar: the
// default calendar (/me) or a named one (/me/calendars/{id}).
func (p *Provider) calendarPath() string {
	if p.creds.CalendarID != "" {
		return "me/calendars/" + p.creds.CalendarID
	}
	return "me"
}

// ----- CalendarProvider -----

// FreeBusy reads the host's own calendar view and returns the busy windows.
// Per Doc 07 INV-7 we surface ONLY start/end of non-free events — never the
// subject/body — by $select-ing just the time + status fields.
func (p *Provider) FreeBusy(ctx context.Context, from, to time.Time) ([]timeutil.Interval, error) {
	headers, err := p.headers(ctx)
	if err != nil {
		return nil, err
	}
	// Force UTC output so start/end come back in UTC regardless of the mailbox tz.
	headers["Prefer"] = `outlook.timezone="UTC"`

	q := url.Values{}
	q.Set("startDateTime", from.UTC().Format("2006-01-02T15:04:05"))
	q.Set("endDateTime", to.UTC().Format("2006-01-02T15:04:05"))
	q.Set("$select", "start,end,showAs,isCancelled")
	q.Set("$orderby", "start/dateTime")
	q.Set("$top", "200")
	next := fmt.Sprintf("%s/v1.0/%s/calendarView?%s", p.cfg.GraphBase, p.calendarPath(), q.Encode())

	out := []timeutil.Interval{}
	for pages := 0; next != ""; pages++ {
		if pages >= maxPages {
			// More pages than we're willing to walk: returning a truncated busy
			// list would let the fail-closed validation free up a genuinely busy
			// window, so surface it as an error instead of a partial success.
			return nil, fmt.Errorf("microsoft: calendarView exceeded %d pages with more results pending", maxPages)
		}
		var resp struct {
			Value []struct {
				Start struct {
					DateTime string `json:"dateTime"`
				} `json:"start"`
				End struct {
					DateTime string `json:"dateTime"`
				} `json:"end"`
				ShowAs      string `json:"showAs"`
				IsCancelled bool   `json:"isCancelled"`
			} `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		if err := httpx.DoJSON(ctx, "GET", next, headers, nil, &resp); err != nil {
			return nil, err
		}
		for _, ev := range resp.Value {
			// "free" events (and cancellations) do not block a slot.
			if ev.IsCancelled || ev.ShowAs == "free" {
				continue
			}
			// A busy event we can't parse (or with end <= start) might be
			// hiding a real conflict; for availability we must not silently
			// drop it — fail so the fail-closed path refuses rather than guesses.
			s, errS := parseGraphTime(ev.Start.DateTime)
			e, errE := parseGraphTime(ev.End.DateTime)
			if errS != nil || errE != nil {
				return nil, fmt.Errorf("microsoft: unparsable busy time (start=%q end=%q)", ev.Start.DateTime, ev.End.DateTime)
			}
			if !e.After(s) {
				return nil, fmt.Errorf("microsoft: busy interval end %s not after start %s", e, s)
			}
			out = append(out, timeutil.Interval{Start: s, End: e})
		}
		next = resp.NextLink // absolute Graph URL, already fully encoded
	}
	return out, nil
}

func (p *Provider) CreateEvent(ctx context.Context, in adapters.CalendarEvent) (adapters.CreatedEvent, error) {
	headers, err := p.headers(ctx)
	if err != nil {
		return adapters.CreatedEvent{}, err
	}
	body := map[string]any{
		"subject": in.Summary,
		"body":    map[string]any{"contentType": "HTML", "content": in.Description},
		"start":   map[string]any{"dateTime": in.StartUTC.Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
		"end":     map[string]any{"dateTime": in.EndUTC.Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
	}
	if in.Location != "" {
		body["location"] = map[string]any{"displayName": in.Location}
	}
	if in.AttendeeMail != "" {
		body["attendees"] = []map[string]any{
			{
				"emailAddress": map[string]any{"address": in.AttendeeMail, "name": in.AttendeeName},
				"type":         "required",
			},
		}
	}
	// Weg 1: create the Teams meeting in the same POST that creates the event.
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
	endpoint := fmt.Sprintf("%s/v1.0/%s/events", p.cfg.GraphBase, p.calendarPath())
	if err := httpx.DoJSON(ctx, "POST", endpoint, headers, body, &resp); err != nil {
		return adapters.CreatedEvent{}, err
	}
	// A 2xx without an event id is a malformed response, not a success: without
	// an id the event can never be updated or deleted, so treat it as an error
	// rather than confirm a booking around an unmanageable external event.
	// (A missing Teams join URL is deliberately NOT turned into an error here:
	// the pipeline keeps the booking confirmed on a provider error and would
	// then drop this id, orphaning the just-created Graph event. Surfacing the
	// missing-link case safely needs pipeline coordination — see review notes.)
	if resp.ID == "" {
		return adapters.CreatedEvent{}, errors.New("microsoft: event created but Graph returned no id")
	}
	// Teams sometimes omits onlineMeeting on the create response while the
	// meeting is still being provisioned. Re-read the event once by id to pick
	// up the join URL before giving up. The event already exists (we hold its
	// id), so this can never orphan anything.
	joinURL := resp.OnlineMeeting.JoinURL
	if in.CreateOnlineMeeting && joinURL == "" {
		if u, ferr := p.fetchJoinURL(ctx, resp.ID); ferr == nil {
			joinURL = u
		}
		if joinURL == "" {
			// The event exists but still has no Teams join link. We can't return
			// an error without orphaning it (the pipeline drops the id on error),
			// so confirm with an empty link — but make the degradation visible
			// rather than silently clearing last_error downstream.
			slog.Warn("microsoft: online meeting created without a Teams join URL",
				"event", resp.ID)
		}
	}
	return adapters.CreatedEvent{
		ExternalID: resp.ID,
		MeetingURL: joinURL,
	}, nil
}

// fetchJoinURL re-reads a just-created event and returns its Teams join URL
// (empty if still not provisioned). Used to recover from Graph omitting
// onlineMeeting on the create response.
func (p *Provider) fetchJoinURL(ctx context.Context, eventID string) (string, error) {
	headers, err := p.headers(ctx)
	if err != nil {
		return "", err
	}
	var resp struct {
		OnlineMeeting struct {
			JoinURL string `json:"joinUrl"`
		} `json:"onlineMeeting"`
	}
	endpoint := fmt.Sprintf("%s/v1.0/%s/events/%s?$select=onlineMeeting", p.cfg.GraphBase, p.calendarPath(), eventID)
	if err := httpx.DoJSON(ctx, "GET", endpoint, headers, nil, &resp); err != nil {
		return "", err
	}
	return resp.OnlineMeeting.JoinURL, nil
}

func (p *Provider) UpdateEvent(ctx context.Context, externalID string, in adapters.CalendarEvent) error {
	headers, err := p.headers(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"subject": in.Summary,
		"start":   map[string]any{"dateTime": in.StartUTC.Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
		"end":     map[string]any{"dateTime": in.EndUTC.Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
	}
	endpoint := fmt.Sprintf("%s/v1.0/%s/events/%s", p.cfg.GraphBase, p.calendarPath(), externalID)
	return httpx.DoJSON(ctx, "PATCH", endpoint, headers, body, nil)
}

func (p *Provider) DeleteEvent(ctx context.Context, externalID string) error {
	headers, err := p.headers(ctx)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/v1.0/%s/events/%s", p.cfg.GraphBase, p.calendarPath(), externalID)
	err = httpx.DoJSON(ctx, "DELETE", endpoint, headers, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// isInvalidGrant reports whether err is a 4xx from the token endpoint whose
// body carries error=invalid_grant (the terminal "re-connect required" case).
func isInvalidGrant(err error) bool {
	var ae *httpx.APIError
	if errors.As(err, &ae) {
		return strings.Contains(ae.Body, "invalid_grant")
	}
	return false
}

// parseGraphTime accepts the timestamps Graph returns
// ("2026-06-01T07:00:00.0000000", with Prefer outlook.timezone="UTC" already
// in UTC). Mirrors the sibling msgraph adapter.
func parseGraphTime(s string) (time.Time, error) {
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

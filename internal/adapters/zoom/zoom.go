// Package zoom implements adapters.MeetingProvider for Zoom via
// Server-to-Server OAuth (account-level app). Each host has their own
// integrations row with the {account_id, client_id, client_secret} of an
// app in their workspace; the adapter exchanges those for an access_token
// and creates a Zoom meeting via POST /v2/users/{userId}/meetings.
//
// Endpoints used:
//
//	POST https://zoom.us/oauth/token?grant_type=account_credentials&account_id=...
//	POST https://api.zoom.us/v2/users/{userId}/meetings
//	DELETE https://api.zoom.us/v2/meetings/{meetingId}
//
// No Zoom SDK — stdlib HTTP + Basic-Auth for the token endpoint.
package zoom

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/adapters/httpx"
)

const Name = "zoom"

// Credentials JSON shape stored encrypted in integrations.credentials.
type Credentials struct {
	AccountID    string `json:"account_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	UserID       string `json:"user_id"` // typically the host's Zoom email
}

type Config struct {
	OAuthBase string `json:"oauth_base,omitempty"`
	APIBase   string `json:"api_base,omitempty"`
}

// Factory is wired into adapters.Registry as a MeetingFactory. Note: Zoom
// is a per-host meeting provider, unlike Jitsi which is instance-scoped.
// The Registry calls MeetingForName with the host's config blob — but
// Zoom needs encrypted credentials per host. v0.3 therefore expects callers
// to pass the decrypted credentials JSON via the conf parameter; future
// versions could plumb a HostMeetingFactory through the registry.
func Factory(credsRaw, confRaw json.RawMessage) (adapters.MeetingProvider, error) {
	var c Credentials
	if err := json.Unmarshal(credsRaw, &c); err != nil {
		return nil, fmt.Errorf("zoom creds: %w", err)
	}
	if c.AccountID == "" || c.ClientID == "" || c.ClientSecret == "" || c.UserID == "" {
		return nil, errors.New("zoom: account_id/client_id/client_secret/user_id required")
	}
	cfg := Config{
		OAuthBase: "https://zoom.us",
		APIBase:   "https://api.zoom.us",
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

func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.tokenExp.Add(-60*time.Second)) {
		return p.token, nil
	}
	// Server-to-Server OAuth: Basic-Auth with client_id:client_secret,
	// grant_type=account_credentials, account_id in query string.
	basic := base64.StdEncoding.EncodeToString([]byte(p.creds.ClientID + ":" + p.creds.ClientSecret))
	url := fmt.Sprintf("%s/oauth/token?grant_type=account_credentials&account_id=%s",
		p.cfg.OAuthBase, p.creds.AccountID)
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := httpx.DoJSON(ctx, "POST", url,
		map[string]string{"Authorization": "Basic " + basic}, nil, &tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", errors.New("zoom: empty access token")
	}
	p.token = tr.AccessToken
	p.tokenExp = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return p.token, nil
}

func (p *Provider) CreateMeeting(ctx context.Context, in adapters.MeetingRequest) (adapters.MeetingResult, error) {
	tok, err := p.accessToken(ctx)
	if err != nil {
		return adapters.MeetingResult{}, err
	}
	headers := map[string]string{"Authorization": "Bearer " + tok}
	body := map[string]any{
		"topic":      in.Summary,
		"type":       2, // scheduled meeting
		"start_time": in.StartUTC.Format(time.RFC3339),
		"duration":   int(in.EndUTC.Sub(in.StartUTC).Minutes()),
		"timezone":   "UTC",
		"settings": map[string]any{
			"join_before_host": true,
			"waiting_room":     false,
		},
	}
	var resp struct {
		ID      json.Number `json:"id"`
		JoinURL string      `json:"join_url"`
	}
	url := fmt.Sprintf("%s/v2/users/%s/meetings", p.cfg.APIBase, p.creds.UserID)
	if err := httpx.DoJSON(ctx, "POST", url, headers, body, &resp); err != nil {
		return adapters.MeetingResult{}, err
	}
	return adapters.MeetingResult{JoinURL: resp.JoinURL, ExternalID: resp.ID.String()}, nil
}

func (p *Provider) DeleteMeeting(ctx context.Context, externalID string) error {
	if externalID == "" {
		return nil
	}
	tok, err := p.accessToken(ctx)
	if err != nil {
		return err
	}
	headers := map[string]string{"Authorization": "Bearer " + tok}
	url := fmt.Sprintf("%s/v2/meetings/%s", p.cfg.APIBase, externalID)
	err = httpx.DoJSON(ctx, "DELETE", url, headers, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

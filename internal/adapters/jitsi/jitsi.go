// Package jitsi implements adapters.MeetingProvider in two modes:
//
//   - "public": stateless URL generation. Idempotent — same booking ID
//     produces the same room URL.
//   - "jwt":    signs a short-lived HS256 JWT room claim, appended as
//     ?jwt=... so a closed Jitsi instance authorises the join.
//
// No third-party JWT library — we sign tokens with stdlib crypto/hmac and
// base64-url-encode them. Keeps the binary small and easy to audit.
package jitsi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/qognio/qognical/internal/adapters"
)

const Name = "jitsi"

// Config maps onto event_types.meeting_config for Jitsi rooms.
type Config struct {
	BaseURL      string `json:"base_url"`
	AppID        string `json:"app_id,omitempty"`
	JWTSecret    string `json:"jwt_secret,omitempty"`    // present → JWT mode
	JWTAlgorithm string `json:"jwt_algorithm,omitempty"` // default HS256
	RoomPrefix   string `json:"room_prefix,omitempty"`   // default "qognical-"
	LifetimeMin  int    `json:"lifetime_min,omitempty"`  // JWT exp; default 240
}

// Factory satisfies adapters.MeetingFactory.
func Factory(_ json.RawMessage, conf json.RawMessage) (adapters.MeetingProvider, error) {
	var c Config
	if len(conf) > 0 {
		if err := json.Unmarshal(conf, &c); err != nil {
			return nil, fmt.Errorf("jitsi config: %w", err)
		}
	}
	if c.BaseURL == "" {
		return nil, errors.New("jitsi base_url required")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.RoomPrefix == "" {
		c.RoomPrefix = "qognical-"
	}
	if c.JWTAlgorithm == "" {
		c.JWTAlgorithm = "HS256"
	}
	if c.LifetimeMin == 0 {
		c.LifetimeMin = 240
	}
	return &Provider{c: c}, nil
}

// Provider is the runtime instance.
type Provider struct{ c Config }

func (p *Provider) Name() string { return Name }

// CreateMeeting returns a deterministic URL. With JWT mode enabled, the URL
// carries a freshly minted token.
func (p *Provider) CreateMeeting(_ context.Context, in adapters.MeetingRequest) (adapters.MeetingResult, error) {
	room := p.c.RoomPrefix + in.BookingID
	u := p.c.BaseURL + "/" + url.PathEscape(room)
	if p.c.JWTSecret != "" {
		tok, err := p.signJWT(room, in)
		if err != nil {
			return adapters.MeetingResult{}, err
		}
		u += "?jwt=" + tok
	}
	return adapters.MeetingResult{JoinURL: u, ExternalID: room}, nil
}

// DeleteMeeting is a no-op (rooms are stateless on both Jitsi modes).
func (p *Provider) DeleteMeeting(_ context.Context, _ string) error { return nil }

// signJWT builds a Jitsi-flavoured HS256 token. Spec is small: header.payload.sig,
// all url-safe base64 without padding.
func (p *Provider) signJWT(room string, in adapters.MeetingRequest) (string, error) {
	if p.c.JWTAlgorithm != "HS256" {
		return "", fmt.Errorf("unsupported jwt_algorithm %q (only HS256 in v1.0)", p.c.JWTAlgorithm)
	}
	now := time.Now().Unix()
	payload := map[string]any{
		"aud":  "jitsi",
		"iss":  p.c.AppID,
		"sub":  p.c.BaseURL,
		"room": room,
		"iat":  now,
		"exp":  now + int64(p.c.LifetimeMin)*60,
		"context": map[string]any{
			"user": map[string]any{
				"name":  in.InviteeName,
				"email": in.InviteeMail,
			},
		},
	}
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(payload)
	enc := func(b []byte) string {
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(hb) + "." + enc(pb)
	mac := hmac.New(sha256.New, []byte(p.c.JWTSecret))
	mac.Write([]byte(signingInput))
	sig := enc(mac.Sum(nil))
	return signingInput + "." + sig, nil
}

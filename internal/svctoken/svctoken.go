// Package svctoken implements the Service-Token mechanism specified in
// ADR-0002. Tokens authorise server-to-server calls into the public-API
// (voice bots, automation runners, CRM bridges) without the captcha + per-IP
// rate limit anonymous flows have.
//
// Tokens are issued by an admin via /api/admin/v1/service-tokens. We return
// the full token once at creation, then persist only its sha256 hash.
// Comparison is constant-time. Tokens can be host-bound (only book on
// behalf of a specific host) and event-type-allowlisted.
package svctoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/qognio/qognical/internal/store"
)

// Scope strings match the values stored in service_tokens.scopes.
type Scope string

const (
	ScopeBookingsCreate    Scope = "bookings:create"
	ScopeBookingsRead      Scope = "bookings:read"
	ScopeBookingsCancel    Scope = "bookings:cancel"
	ScopeBookingsReschedule Scope = "bookings:reschedule"
	ScopeAvailabilityRead  Scope = "availability:read"
	ScopeEventTypesRead    Scope = "event_types:read"
)

// Prefix is the user-visible token prefix; helps Stripe-style identification
// when a key leaks into a log.
const Prefix = "qg_svc_"

// Generate returns a freshly-rolled token and its hash. The caller persists
// the hash in service_tokens.token_hash; the raw token is shown to the user
// exactly once.
func Generate() (raw string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = Prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
	h := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(h[:]), nil
}

// Hash returns the storage representation of a raw token.
func Hash(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// ServiceToken is the resolved store representation.
type ServiceToken struct {
	ID                string
	Name              string
	Scopes            []Scope
	HostBinding       string   // empty = any host
	EventTypeAllowlist []string // nil/empty = any event type
	ExpiresAt         time.Time
	RevokedAt         time.Time
}

// Resolved indicates which token a request maps to plus what it can do.
type Resolved struct {
	Token    ServiceToken
	RawValue string
}

// HasScope returns true if the resolved token carries the requested scope.
func (r *Resolved) HasScope(s Scope) bool {
	for _, x := range r.Token.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

// CanBookFor checks host_binding + event_type_allowlist gating.
func (r *Resolved) CanBookFor(hostID, eventTypeID string) bool {
	if r.Token.HostBinding != "" && r.Token.HostBinding != hostID {
		return false
	}
	if len(r.Token.EventTypeAllowlist) == 0 {
		return true
	}
	for _, id := range r.Token.EventTypeAllowlist {
		if id == eventTypeID {
			return true
		}
	}
	return false
}

// Errors returned by Verify.
var (
	ErrTokenMissing  = errors.New("service token missing")
	ErrTokenInvalid  = errors.New("service token invalid")
	ErrTokenExpired  = errors.New("service token expired")
	ErrTokenRevoked  = errors.New("service token revoked")
)

// Verify reads the bearer token from `Authorization`, looks up the hash, and
// returns Resolved when valid. Touches `last_used_at` so audits can show
// dormant tokens. The token is identified by its full hash — there's no
// timing-attack window on the input string.
func Verify(repo *store.Repo, header string) (*Resolved, error) {
	if header == "" {
		return nil, ErrTokenMissing
	}
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(header, bearerPrefix) {
		return nil, ErrTokenInvalid
	}
	raw := strings.TrimSpace(header[len(bearerPrefix):])
	if !strings.HasPrefix(raw, Prefix) {
		return nil, ErrTokenInvalid
	}
	st, found, err := repo.FindServiceTokenByHash(Hash(raw))
	if err != nil || !found {
		return nil, ErrTokenInvalid
	}
	if !st.RevokedAt.IsZero() {
		return nil, ErrTokenRevoked
	}
	if !st.ExpiresAt.IsZero() && time.Now().After(st.ExpiresAt) {
		return nil, ErrTokenExpired
	}
	_ = repo.TouchServiceTokenLastUsed(st.ID)
	scopes := make([]Scope, 0, len(st.Scopes))
	for _, s := range st.Scopes {
		scopes = append(scopes, Scope(s))
	}
	return &Resolved{
		Token: ServiceToken{
			ID: st.ID, Name: st.Name, Scopes: scopes,
			HostBinding: st.HostBinding,
			EventTypeAllowlist: st.EventTypeAllowlist,
			ExpiresAt: st.ExpiresAt, RevokedAt: st.RevokedAt,
		},
		RawValue: raw,
	}, nil
}

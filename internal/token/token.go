// Package token issues and verifies the signed URL tokens used for invitee
// actions on bookings (Cancel/Reschedule/View). Single-use semantics are
// enforced by storing only a hash of the latest valid token's signature in
// bookings.cancel_token_hash — when the action succeeds the caller rotates
// the field, which invalidates the old token (INV-8 / TOK-4).
//
// Wire format: <base64url-payload>.<base64url-sig>
//
// Payload is "<booking_id>|<action>|<expires_unix>".
package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/qognio/qognical/internal/crypto"
)

// Action enumerates the operations a token can authorise.
type Action string

const (
	ActionCancel     Action = "cancel"
	ActionReschedule Action = "reschedule"
	ActionView       Action = "view"
)

// DefaultLifetime is used by Issue when caller passes a zero duration.
const DefaultLifetime = 365 * 24 * time.Hour

// Token is the issued artefact: the user-visible string plus the hash to store.
type Token struct {
	String string // give this to the invitee (URL parameter)
	Hash   string // store this in bookings.cancel_token_hash
}

// Service wraps the master key. Build once at app startup.
type Service struct {
	master *crypto.Master
}

func New(m *crypto.Master) *Service {
	return &Service{master: m}
}

// Issue produces a fresh token for (bookingID, action). lifetime defaults to
// DefaultLifetime when zero.
func (s *Service) Issue(bookingID string, action Action, lifetime time.Duration) (Token, error) {
	if bookingID == "" {
		return Token{}, errors.New("booking id required")
	}
	if !validAction(action) {
		return Token{}, fmt.Errorf("unknown action %q", action)
	}
	if lifetime == 0 {
		lifetime = DefaultLifetime
	}
	expiry := time.Now().Add(lifetime).Unix()
	// 8-byte nonce so two tokens issued in the same second are still distinct
	// — important for rotation (TOK-4) and for unit-test determinism.
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return Token{}, err
	}
	payload := fmt.Sprintf("%s|%s|%d|%s", bookingID, action, expiry, hex.EncodeToString(nonce))
	sig, err := s.master.SignURLToken([]byte(payload))
	if err != nil {
		return Token{}, err
	}
	str := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
	return Token{String: str, Hash: hashSig(sig)}, nil
}

// Verify parses, signature-checks, and expiry-checks the token. Returns the
// booking ID and action when valid. ExpectedHash, if non-empty, must equal
// the hash of the token's signature — used to enforce single-use after
// rotation.
func (s *Service) Verify(token string, expectedHash string) (bookingID string, action Action, err error) {
	payload, sig, err := splitToken(token)
	if err != nil {
		return "", "", ErrInvalid
	}
	ok, err := s.master.VerifyURLToken(payload, sig)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", ErrInvalid
	}
	bookingID, action, expiry, err := parsePayload(payload)
	if err != nil {
		return "", "", ErrInvalid
	}
	if time.Now().Unix() > expiry {
		return "", "", ErrExpired
	}
	if expectedHash != "" {
		// constant-time string compare.
		if !hmac.Equal([]byte(expectedHash), []byte(hashSig(sig))) {
			return "", "", ErrAlreadyUsed
		}
	}
	return bookingID, action, nil
}

// Sentinel errors map to the API error codes in Doc 06.
var (
	ErrInvalid     = errors.New("token invalid")
	ErrExpired     = errors.New("token expired")
	ErrAlreadyUsed = errors.New("token already used")
)

func validAction(a Action) bool {
	switch a {
	case ActionCancel, ActionReschedule, ActionView:
		return true
	}
	return false
}

func splitToken(s string) ([]byte, []byte, error) {
	parts := strings.SplitN(s, ".", 2)
	if len(parts) != 2 {
		return nil, nil, errors.New("malformed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, err
	}
	return payload, sig, nil
}

func parsePayload(p []byte) (string, Action, int64, error) {
	parts := strings.SplitN(string(p), "|", 4)
	if len(parts) != 4 {
		return "", "", 0, errors.New("payload shape")
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", 0, err
	}
	return parts[0], Action(parts[1]), exp, nil
}

func hashSig(sig []byte) string {
	h := sha256.Sum256(sig)
	return hex.EncodeToString(h[:])
}

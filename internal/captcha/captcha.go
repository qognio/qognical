// Package captcha verifies captcha tokens with the configured provider.
// We support hCaptcha and Cloudflare Turnstile out of the box (both use the
// same shape — POST to verify endpoint, JSON response with "success" bool).
// reCAPTCHA is intentionally NOT supported (DSGVO posture, see Doc 06).
//
// The booking-API calls Verifier.Verify on every anonymous mutation; service-
// token callers bypass it entirely. When QOGNICAL_CAPTCHA_PROVIDER is empty
// the booking-API uses a permissive no-op so dev/internal instances can run
// without any captcha config.
package captcha

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/qognio/qognical/internal/adapters/httpx"
)

// Verifier is the small interface the API package imports.
type Verifier interface {
	Verify(ctx string, token string, remoteIP string) error
}

// NoopVerifier accepts any token. Used when no provider is configured.
type NoopVerifier struct{}

func (NoopVerifier) Verify(_, _, _ string) error { return nil }

// New returns the configured verifier or a Noop when provider is empty.
func New(provider, secret string) Verifier {
	switch strings.ToLower(provider) {
	case "", "none":
		return NoopVerifier{}
	case "hcaptcha":
		return &httpVerifier{
			name:       "hcaptcha",
			endpoint:   "https://api.hcaptcha.com/siteverify",
			secret:     secret,
			scoreField: "",
		}
	case "turnstile":
		return &httpVerifier{
			name:       "turnstile",
			endpoint:   "https://challenges.cloudflare.com/turnstile/v0/siteverify",
			secret:     secret,
			scoreField: "",
		}
	}
	// Unknown provider configured — fail closed.
	return &errorVerifier{err: fmt.Errorf("unknown captcha provider %q", provider)}
}

// errorVerifier always returns the wrapped error. Lets misconfiguration
// surface on first request instead of starting up silently in noop mode.
type errorVerifier struct{ err error }

func (e *errorVerifier) Verify(string, string, string) error { return e.err }

// httpVerifier covers the hCaptcha + Turnstile shape.
type httpVerifier struct {
	name       string
	endpoint   string
	secret     string
	scoreField string
}

func (v *httpVerifier) Verify(_ string, token string, remoteIP string) error {
	if token == "" {
		return errors.New("captcha token empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	form := url.Values{}
	form.Set("secret", v.secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	var resp struct {
		Success     bool     `json:"success"`
		ErrorCodes  []string `json:"error-codes"`
		ChallengeTS string   `json:"challenge_ts"`
	}
	// httpx.DoForm only takes a map[string]string. Translate.
	asMap := make(map[string]string, len(form))
	for k, vs := range form {
		if len(vs) > 0 {
			asMap[k] = vs[0]
		}
	}
	if err := httpx.DoForm(ctx, v.endpoint, asMap, &resp); err != nil {
		return fmt.Errorf("%s verify: %w", v.name, err)
	}
	if !resp.Success {
		return fmt.Errorf("%s rejected: %v", v.name, resp.ErrorCodes)
	}
	return nil
}

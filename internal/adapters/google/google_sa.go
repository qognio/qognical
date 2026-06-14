package google

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

const calendarScope = "https://www.googleapis.com/auth/calendar"

// signJWT builds and RS256-signs a JWT-bearer assertion for the service-account
// flow (RFC 7523). The SA authenticates as itself (no `sub` impersonation),
// which is the correct mode for a consumer calendar that has been shared
// directly with the SA's client_email. `now` is a parameter for testability.
func (p *Provider) signJWT(now time.Time) (string, error) {
	key, err := parseRSAPrivateKey(p.creds.PrivateKey)
	if err != nil {
		return "", err
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT"}
	if p.creds.PrivateKeyID != "" {
		header["kid"] = p.creds.PrivateKeyID
	}
	aud := p.creds.TokenURI
	if aud == "" {
		aud = "https://oauth2.googleapis.com/token"
	}
	claims := map[string]any{
		"iss":   p.creds.ClientEmail,
		"scope": calendarScope,
		"aud":   aud,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := b64url(hb) + "." + b64url(cb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("rsa sign: %w", err)
	}
	return signingInput + "." + b64url(sig), nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemStr)))
	if block == nil {
		return nil, errors.New("google: private_key is not valid PEM")
	}
	// Google SA keys are PKCS#8; fall back to PKCS#1 for robustness.
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("google: private_key is not an RSA key")
		}
		return rk, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

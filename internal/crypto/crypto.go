// Package crypto handles at-rest encryption of credential fields and derives
// purpose-specific sub-keys from a single master key per ADR/07 spec.
//
// Master key comes from QOGNICAL_ENCRYPTION_KEY (32 bytes, base64) or the
// _FILE variant. HKDF (RFC 5869) derives sub-keys for distinct purposes
// (credential encryption, URL-token signing, webhook signing) so a leak of
// one sub-key cannot compromise the others.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	masterKeyLen = 32
	gcmNonceLen  = 12

	PurposeCredential = "qognical/credential-encryption/v1"
	PurposeURLToken   = "qognical/url-token-signing/v1"
	PurposeWebhook    = "qognical/outbound-webhook-signing/v1"
)

// Master holds the decoded master key. Use NewMaster to construct.
type Master struct {
	key []byte
}

// NewMaster decodes a base64-encoded 32-byte key.
func NewMaster(b64 string) (*Master, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("master key not valid base64: %w", err)
	}
	if len(raw) != masterKeyLen {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", masterKeyLen, len(raw))
	}
	return &Master{key: raw}, nil
}

// SubKey derives a 32-byte sub-key for the given purpose label using HKDF-SHA256.
// The purpose string acts as the HKDF "info" parameter; salt is empty.
func (m *Master) SubKey(purpose string) ([]byte, error) {
	if len(purpose) == 0 {
		return nil, errors.New("purpose must not be empty")
	}
	out := make([]byte, 32)
	kdf := hkdf.New(sha256.New, m.key, nil, []byte(purpose))
	if _, err := io.ReadFull(kdf, out); err != nil {
		return nil, fmt.Errorf("hkdf read: %w", err)
	}
	return out, nil
}

// Cipher returns an AES-GCM cipher seeded with the credential sub-key.
func (m *Master) Cipher() (cipher.AEAD, error) {
	sub, err := m.SubKey(PurposeCredential)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(sub)
	if err != nil {
		return nil, fmt.Errorf("aes init: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm init: %w", err)
	}
	return aead, nil
}

// Encrypt seals plaintext with AES-GCM using the credential sub-key. Output
// layout: nonce || ciphertext || tag (the GCM tag is appended to ciphertext
// by the stdlib), base64-encoded for safe text storage.
func (m *Master) Encrypt(plaintext []byte) (string, error) {
	aead, err := m.Cipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcmNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	sealed := aead.Seal(nil, nonce, plaintext, nil)
	out := append(nonce, sealed...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt reverses Encrypt.
func (m *Master) Decrypt(b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(raw) < gcmNonceLen+16 {
		return nil, errors.New("ciphertext too short")
	}
	aead, err := m.Cipher()
	if err != nil {
		return nil, err
	}
	nonce, ct := raw[:gcmNonceLen], raw[gcmNonceLen:]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return pt, nil
}

// SignURLToken returns an HMAC-SHA256 over the input using the URL-token sub-key.
// Token format spec lives in docs/planning/06-public-api-spec.md.
func (m *Master) SignURLToken(payload []byte) ([]byte, error) {
	sub, err := m.SubKey(PurposeURLToken)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, sub)
	mac.Write(payload)
	return mac.Sum(nil), nil
}

// VerifyURLToken is constant-time.
func (m *Master) VerifyURLToken(payload, sig []byte) (bool, error) {
	expected, err := m.SignURLToken(payload)
	if err != nil {
		return false, err
	}
	return hmac.Equal(expected, sig), nil
}

// SignWebhook returns an HMAC-SHA256 over the body using the outbound-webhook
// sub-key. Used in the X-Qognical-Signature header per spec 05.
func (m *Master) SignWebhook(body []byte) ([]byte, error) {
	sub, err := m.SubKey(PurposeWebhook)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, sub)
	mac.Write(body)
	return mac.Sum(nil), nil
}

// GenerateMasterKey returns a freshly generated base64-encoded master key.
// For operator convenience (CLI: qognical genkey).
func GenerateMasterKey() (string, error) {
	raw := make([]byte, masterKeyLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

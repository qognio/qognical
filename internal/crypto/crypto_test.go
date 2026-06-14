package crypto

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	k, err := GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMaster(k)
	if err != nil {
		t.Fatal(err)
	}

	plain := []byte("stripe_secret_key=sk_live_abc123")
	ct, err := m.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(ct), plain) {
		t.Fatal("ciphertext contains plaintext")
	}
	pt, err := m.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, plain) {
		t.Fatalf("roundtrip mismatch: got %q", pt)
	}
}

func TestSubKeysAreDistinct(t *testing.T) {
	k, _ := GenerateMasterKey()
	m, _ := NewMaster(k)

	cred, _ := m.SubKey(PurposeCredential)
	url, _ := m.SubKey(PurposeURLToken)
	wh, _ := m.SubKey(PurposeWebhook)

	if bytes.Equal(cred, url) || bytes.Equal(cred, wh) || bytes.Equal(url, wh) {
		t.Fatal("sub-keys must differ per purpose")
	}
}

func TestVerifyURLToken(t *testing.T) {
	k, _ := GenerateMasterKey()
	m, _ := NewMaster(k)

	payload := []byte("booking_id=abc;action=cancel;exp=1234567890")
	sig, _ := m.SignURLToken(payload)

	ok, _ := m.VerifyURLToken(payload, sig)
	if !ok {
		t.Fatal("valid signature should verify")
	}

	tampered := append([]byte{}, payload...)
	tampered[0] ^= 1
	ok, _ = m.VerifyURLToken(tampered, sig)
	if ok {
		t.Fatal("tampered payload must not verify")
	}
}

func TestRejectsShortKey(t *testing.T) {
	if _, err := NewMaster("dGVzdA=="); err == nil {
		t.Fatal("4-byte key should be rejected")
	}
}

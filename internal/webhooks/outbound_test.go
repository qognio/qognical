package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func TestVerifyOutboundSignatureRoundtrip(t *testing.T) {
	secret := "hush"
	body := []byte(`{"event_id":"evt_1","event_type":"booking.confirmed"}`)
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	headers := map[string]string{"X-Qognical-Signature": "t=" + ts + ",v1=" + sig}
	if err := VerifyOutboundSignature(secret, headers, body, 5); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
}

func TestVerifyOutboundSignatureWrongSecret(t *testing.T) {
	body := []byte("x")
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("hush"))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	if err := VerifyOutboundSignature("wrong", map[string]string{
		"X-Qognical-Signature": "t=" + ts + ",v1=" + sig,
	}, body, 5); err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestNextRetrySchedule(t *testing.T) {
	first := nextRetry(1)
	if d := time.Until(first); d < 55*time.Second || d > 65*time.Second {
		t.Errorf("attempt 1 retry should be ~1m, got %v", d)
	}
	fifth := nextRetry(5)
	if d := time.Until(fifth); d < 11*time.Hour || d > 13*time.Hour {
		t.Errorf("attempt 5 retry should be ~12h, got %v", d)
	}
}

package api

import (
	"strings"
	"testing"
	"time"
)

func newMSOAuth() *MSOAuth {
	return &MSOAuth{StateKey: []byte("test-encryption-key-material")}
}

// A freshly signed state round-trips back to the exact host id, and the HMAC
// uses the derived (domain-separated) key — not the raw StateKey.
func TestSignVerifyStateRoundTrip(t *testing.T) {
	m := newMSOAuth()
	host := "host_abc123"
	got, err := m.verifyState(m.signState(host, time.Now().Add(10*time.Minute).Unix()))
	if err != nil {
		t.Fatalf("verifyState errored on a valid state: %v", err)
	}
	if got != host {
		t.Fatalf("round-trip host = %q, want %q", got, host)
	}
}

func TestVerifyStateRejectsExpired(t *testing.T) {
	m := newMSOAuth()
	if _, err := m.verifyState(m.signState("h", time.Now().Add(-time.Second).Unix())); err == nil {
		t.Fatal("expected expired state to be rejected")
	}
}

// A tampered payload (attacker swaps in a different host id) must fail the HMAC
// check — this is what stops a forged state from binding consent to any host.
func TestVerifyStateRejectsTamper(t *testing.T) {
	m := newMSOAuth()
	valid := m.signState("victim", time.Now().Add(10*time.Minute).Unix())
	// Flip the signature segment.
	parts := strings.SplitN(valid, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected state shape %q", valid)
	}
	tampered := parts[0] + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := m.verifyState(tampered); err == nil {
		t.Fatal("expected tampered signature to be rejected")
	}
}

// State signed under one StateKey must not verify under another — confirms the
// signature actually depends on the key.
func TestVerifyStateRejectsForeignKey(t *testing.T) {
	a := newMSOAuth()
	b := &MSOAuth{StateKey: []byte("a-completely-different-key")}
	if _, err := b.verifyState(a.signState("h", time.Now().Add(10*time.Minute).Unix())); err == nil {
		t.Fatal("state signed under a different key must not verify")
	}
}

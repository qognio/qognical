package svctoken

import (
	"strings"
	"testing"
)

func TestGenerateIsUnique(t *testing.T) {
	a, ah, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, Prefix) {
		t.Errorf("token %q missing prefix", a)
	}
	if len(a) < 50 {
		t.Errorf("token suspiciously short: %d chars", len(a))
	}
	b, bh, _ := Generate()
	if a == b || ah == bh {
		t.Fatal("duplicate tokens from Generate")
	}
}

func TestHashIsDeterministic(t *testing.T) {
	raw, h, _ := Generate()
	if Hash(raw) != h {
		t.Fatal("Hash() not deterministic with Generate output")
	}
	if Hash(raw) == Hash(raw+"x") {
		t.Fatal("Hash collision on near-duplicate input")
	}
}

func TestResolvedScopeAndBookGating(t *testing.T) {
	r := &Resolved{Token: ServiceToken{
		Scopes:            []Scope{ScopeBookingsCreate, ScopeBookingsRead},
		HostBinding:       "host_42",
		EventTypeAllowlist: []string{"et_1", "et_2"},
	}}
	if !r.HasScope(ScopeBookingsCreate) {
		t.Error("scope check false-negative")
	}
	if r.HasScope(ScopeBookingsCancel) {
		t.Error("scope check false-positive")
	}
	if r.CanBookFor("host_99", "et_1") {
		t.Error("host_binding not enforced")
	}
	if r.CanBookFor("host_42", "et_99") {
		t.Error("event_type allowlist not enforced")
	}
	if !r.CanBookFor("host_42", "et_2") {
		t.Error("legitimate booking incorrectly blocked")
	}
}

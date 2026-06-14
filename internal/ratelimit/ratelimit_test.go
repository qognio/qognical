package ratelimit

import (
	"testing"
	"time"
)

func TestAllowUnderBurst(t *testing.T) {
	l, _ := New("5/sec")
	for i := 0; i < 5; i++ {
		ok, _ := l.Allow("1.2.3.4")
		if !ok {
			t.Fatalf("burst token %d should be allowed", i)
		}
	}
	ok, wait := l.Allow("1.2.3.4")
	if ok {
		t.Fatal("6th request must be blocked")
	}
	if wait <= 0 {
		t.Errorf("retry-after = %v, want positive", wait)
	}
}

func TestPerKeyIsolation(t *testing.T) {
	l, _ := New("1/sec")
	if ok, _ := l.Allow("a"); !ok { t.Fatal("a #1") }
	if ok, _ := l.Allow("b"); !ok { t.Fatal("b #1") }
	if ok, _ := l.Allow("a"); ok { t.Fatal("a #2 should be blocked") }
}

func TestNilLimiterAllowsAll(t *testing.T) {
	var l *Limiter
	for i := 0; i < 100; i++ {
		if ok, _ := l.Allow("x"); !ok {
			t.Fatal("nil limiter must allow")
		}
	}
}

func TestEmptySpecAllowsAll(t *testing.T) {
	l, err := New("")
	if err != nil { t.Fatal(err) }
	for i := 0; i < 100; i++ {
		if ok, _ := l.Allow("x"); !ok {
			t.Fatal("empty spec must allow")
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, s := range []string{"abc", "5/decade", "0/sec"} {
		if _, err := New(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}

func TestTokenRefillOverTime(t *testing.T) {
	l, _ := New("60/min") // 1 per second
	for i := 0; i < 60; i++ {
		l.Allow("k")
	}
	if ok, _ := l.Allow("k"); ok {
		t.Fatal("bucket should be empty after 60 hits")
	}
	// Wait briefly to refill a token.
	time.Sleep(1100 * time.Millisecond)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("should refill after 1.1s")
	}
}

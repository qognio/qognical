// Package ratelimit is a tiny in-process token-bucket per IP. Doc 06 says
// "60/min" for public-read, "5/min" for mutations; we keep both knobs as
// configurable strings (parse "N/min" or "N/sec"). One instance per limiter
// scope; the api package holds two.
//
// In-process by design: qognical instances are single-process per Doc 03;
// horizontal scaling = separate tenant per deployment, so no shared store
// is needed. If that changes we'd plug in Redis here.
package ratelimit

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limiter is safe for concurrent use by HTTP handlers.
type Limiter struct {
	rate     float64 // tokens added per second
	burst    float64 // max bucket size
	mu       sync.Mutex
	buckets  map[string]*bucket
	lastGC   time.Time
	gcEvery  time.Duration
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New parses spec ("N/min" or "N/sec") and returns a Limiter. Empty spec
// means "unlimited" — useful for tests and dev.
func New(spec string) (*Limiter, error) {
	if spec == "" {
		return &Limiter{rate: 0}, nil
	}
	parts := strings.SplitN(spec, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("rate-limit spec %q invalid (want N/min or N/sec)", spec)
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("rate-limit count %q invalid", parts[0])
	}
	var perSec float64
	switch strings.TrimSpace(strings.ToLower(parts[1])) {
	case "sec", "second", "s":
		perSec = float64(n)
	case "min", "minute", "m":
		perSec = float64(n) / 60.0
	case "hour", "h":
		perSec = float64(n) / 3600.0
	default:
		return nil, fmt.Errorf("rate-limit window %q unsupported", parts[1])
	}
	return &Limiter{
		rate:    perSec,
		burst:   float64(n),
		buckets: make(map[string]*bucket),
		gcEvery: 10 * time.Minute,
	}, nil
}

// Allow consumes a token for the given key (typically client IP) and returns
// (allowed, retryAfter). retryAfter is non-zero when allowed=false.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil || l.rate <= 0 {
		return true, 0
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = make(map[string]*bucket)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens = clamp(b.tokens+elapsed*l.rate, 0, l.burst)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		l.gcLocked(now)
		return true, 0
	}
	missing := 1 - b.tokens
	wait := time.Duration(missing/l.rate*float64(time.Second)) + time.Second
	l.gcLocked(now)
	return false, wait
}

func (l *Limiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < l.gcEvery {
		return
	}
	l.lastGC = now
	cutoff := now.Add(-30 * time.Minute)
	for k, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

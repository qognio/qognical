// Package webhooks handles outbound-webhook delivery (qognical → external
// CRM/automation) and inbound payment-webhook routing (Stripe/PayPal →
// qognical). Both directions live in the same package because they share
// helpers (raw-body handling, signature constants, audit logging).
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/qognio/qognical/internal/adapters/httpx"
	"github.com/qognio/qognical/internal/store"
)

// Event names mirror docs/planning/05.
const (
	EventBookingCreated     = "booking.created"
	EventBookingConfirmed   = "booking.confirmed"
	EventBookingCancelled   = "booking.cancelled"
	EventBookingRescheduled = "booking.rescheduled"
	EventBookingRefunded    = "booking.refunded"
	EventPaymentFailed      = "payment.failed"
)

// Dispatcher enqueues outbound deliveries for every active subscriber. The
// actual HTTP request happens asynchronously in the cron loop.
type Dispatcher struct {
	repo *store.Repo
}

func NewDispatcher(repo *store.Repo) *Dispatcher { return &Dispatcher{repo: repo} }

// Emit enqueues delivery rows for every active subscriber matching the
// host+event. Non-fatal: failures are logged in the calling pipeline step
// but don't roll back the booking.
func (d *Dispatcher) Emit(hostID, event string, data map[string]any) error {
	hooks, err := d.repo.ActiveOutboundWebhooksForEvent(hostID, event)
	if err != nil {
		return err
	}
	if len(hooks) == 0 {
		return nil
	}
	envelope := map[string]any{
		"event_id":    newEventID(),
		"event_type":  event,
		"occurred_at": time.Now().UTC().Format(time.RFC3339),
		"data":        data,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	for _, h := range hooks {
		if err := d.repo.EnqueueWebhookDelivery(h.ID, envelope["event_id"].(string), payload); err != nil {
			return err
		}
	}
	return nil
}

// RunDeliveries pulls pending/failed deliveries and pushes them. Called by
// a 30-second cron tick.
func (d *Dispatcher) RunDeliveries(ctx context.Context) {
	pendings, err := d.repo.PendingWebhookDeliveries(50)
	if err != nil {
		return
	}
	for _, p := range pendings {
		d.deliverOne(ctx, p)
	}
}

func (d *Dispatcher) deliverOne(ctx context.Context, dl store.WebhookDelivery) {
	hook, err := d.repo.FindOutboundWebhookByID(dl.WebhookID)
	if err != nil {
		_ = d.repo.UpdateWebhookDeliveryResult(dl.ID, "abandoned", dl.Attempts+1, 0, time.Time{})
		return
	}
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(hook.Secret))
	mac.Write([]byte(ts + "."))
	mac.Write(dl.Payload)
	sig := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, "POST", hook.URL, bytes.NewReader(dl.Payload))
	if err != nil {
		_ = d.repo.UpdateWebhookDeliveryResult(dl.ID, "failed", dl.Attempts+1, 0, nextRetry(dl.Attempts+1))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Qognical-Signature", "t="+ts+",v1="+sig)
	req.Header.Set("X-Qognical-Event-Id", dl.EventID)
	req.Header.Set("X-Qognical-Timestamp", ts)
	resp, err := httpx.DefaultClient.Do(req)
	if err != nil {
		_ = d.repo.UpdateWebhookDeliveryResult(dl.ID, "failed", dl.Attempts+1, 0, nextRetry(dl.Attempts+1))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = d.repo.UpdateWebhookDeliveryResult(dl.ID, "delivered", dl.Attempts+1, resp.StatusCode, time.Time{})
		return
	}
	attempts := dl.Attempts + 1
	status := "failed"
	var next time.Time
	if attempts >= 6 {
		status = "abandoned" // Doc 05: 1m, 5m, 30m, 2h, 12h, then abandoned
	} else {
		next = nextRetry(attempts)
	}
	_ = d.repo.UpdateWebhookDeliveryResult(dl.ID, status, attempts, resp.StatusCode, next)
}

// nextRetry implements the 1m/5m/30m/2h/12h schedule from Doc 05.
func nextRetry(attempt int) time.Time {
	delays := []time.Duration{
		1 * time.Minute,
		5 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		12 * time.Hour,
	}
	if attempt > len(delays) {
		attempt = len(delays)
	}
	return time.Now().UTC().Add(delays[attempt-1])
}

func newEventID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "evt_" + hex.EncodeToString(b)
}

// VerifyOutboundSignature reverses the sign step above. Exported so receivers
// (e.g. n8n flows) can be tested against the same code.
func VerifyOutboundSignature(secret string, headers map[string]string, body []byte, maxAgeMin int) error {
	sig := headers["X-Qognical-Signature"]
	if sig == "" {
		return fmt.Errorf("missing X-Qognical-Signature")
	}
	var ts, expected string
	for _, part := range splitComma(sig) {
		kv := splitEq(part)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			expected = kv[1]
		}
	}
	if ts == "" || expected == "" {
		return fmt.Errorf("malformed signature header")
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return err
	}
	if maxAgeMin > 0 {
		if time.Since(time.Unix(tsInt, 0)) > time.Duration(maxAgeMin)*time.Minute {
			return fmt.Errorf("signature too old")
		}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	got := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(expected)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// splitComma + splitEq are inline helpers (avoiding the strings import here
// keeps the package's imports minimal).
func splitComma(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func splitEq(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

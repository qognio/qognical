package webhooks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/state"
	"github.com/qognio/qognical/internal/store"
)

// Inbound exposes /webhooks/stripe and /webhooks/paypal. The handler reads
// the raw body once, hands it to the adapter for signature verification,
// dedupes via audit_log, and dispatches the typed event into the booking
// pipeline.
type Inbound struct {
	Repo            *store.Repo
	Stripe          adapters.PaymentProvider // optional
	PayPal          adapters.PaymentProvider // optional
	OnPaymentResult func(ctx context.Context, ev adapters.WebhookEvent) error // glue into pipeline
}

func (i *Inbound) Register(se *core.ServeEvent) {
	if i.Stripe != nil {
		se.Router.POST("/webhooks/stripe", i.handle(i.Stripe, "stripe"))
	}
	if i.PayPal != nil {
		se.Router.POST("/webhooks/paypal", i.handle(i.PayPal, "paypal"))
	}
}

func (i *Inbound) handle(prov adapters.PaymentProvider, name string) func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		raw, err := io.ReadAll(e.Request.Body)
		if err != nil {
			return e.NoContent(http.StatusBadRequest)
		}
		_ = e.Request.Body.Close()
		hdrs := make(adapters.WebhookHeaders, len(e.Request.Header))
		for k, v := range e.Request.Header {
			if len(v) > 0 {
				hdrs[k] = v[0]
			}
		}
		ev, err := prov.VerifyWebhook(raw, hdrs)
		if err != nil {
			slog.Warn("webhook signature invalid", "provider", name, "err", err)
			return e.NoContent(http.StatusBadRequest)
		}
		// Idempotency: skip if we've seen this event_id before.
		seen, _ := i.Repo.HasProcessedPaymentEvent(name, ev.EventID)
		if seen {
			return e.JSON(http.StatusOK, map[string]string{"status": "duplicate"})
		}
		_ = i.Repo.MarkPaymentEventProcessed(name, ev.EventID, ev.BookingID, map[string]any{
			"type": string(ev.Type), "external_id": ev.ExternalID,
		})
		// Respond immediately (Doc 09: <20s SLA), do the heavy work async.
		go func() {
			ctx, cancel := contextBg()
			defer cancel()
			if i.OnPaymentResult != nil {
				if err := i.OnPaymentResult(ctx, ev); err != nil {
					slog.Warn("webhook handler failed", "provider", name, "err", err)
				}
			}
		}()
		return e.JSON(http.StatusOK, map[string]string{"status": "queued"})
	}
}

// DispatchPayment is the default mapping from a verified payment event onto
// the booking's payment state and downstream effects. Provided as a helper
// for main.go to wire in; tests can wrap or replace.
func DispatchPayment(repo *store.Repo, ev adapters.WebhookEvent, onConfirmed func(bookingID string) error) error {
	// Some events (charge.refunded) don't carry client_reference_id — look up
	// the booking by payment_external_id instead. P3-I10.
	if ev.BookingID == "" && ev.ExternalID != "" {
		if b, err := repo.FindBookingByPaymentExternalID(ev.ExternalID); err == nil {
			ev.BookingID = b.ID
		}
	}
	if ev.BookingID == "" {
		return errors.New("webhook missing booking id")
	}
	switch ev.Type {
	case adapters.EventPaymentSucceeded:
		_, err := repo.SetBookingPaymentResult(ev.BookingID, state.StatusConfirmed, "paid", ev.ExternalID, ev.AmountCents)
		if err != nil {
			return err
		}
		if onConfirmed != nil {
			return onConfirmed(ev.BookingID)
		}
	case adapters.EventPaymentExpired:
		_, _ = repo.SetBookingPaymentResult(ev.BookingID, state.StatusAbandoned, "failed", ev.ExternalID, 0)
	case adapters.EventPaymentFailed:
		_, _ = repo.SetBookingPaymentResult(ev.BookingID, state.StatusAbandoned, "failed", ev.ExternalID, 0)
	case adapters.EventPaymentRefunded:
		_, _ = repo.SetBookingPaymentResult(ev.BookingID, state.StatusRefunded, "refunded", ev.ExternalID, 0)
	}
	return nil
}

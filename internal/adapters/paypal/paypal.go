// Package paypal implements adapters.PaymentProvider against PayPal Orders v2.
// No PayPal SDK — pure net/http. Auth is the standard client-credentials
// flow; webhook verification calls PayPal's verify-webhook-signature endpoint
// (the "official" way, which works for both Sandbox and Live).
//
// Modes supported in v1.0 per docs/planning/05:
//   - fixed   : single purchase_unit, capture on completion
//   - deposit : same shape, partial amount (rest outside the system)
//   - open    : caller passes the amount
//
// Not in v1.0: subscription (use Stripe), hold (PayPal supports auth+capture
// but the UX is awkward; defer to v1.x).
package paypal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/adapters/httpx"
)

const Name = "paypal"

type Credentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	WebhookID    string `json:"webhook_id"`
	Mode         string `json:"mode"` // "sandbox" | "live"
}

type Config struct {
	APIBase string `json:"api_base,omitempty"`
}

func Factory(credsRaw, confRaw json.RawMessage) (adapters.PaymentProvider, error) {
	var c Credentials
	if err := json.Unmarshal(credsRaw, &c); err != nil {
		return nil, fmt.Errorf("paypal creds: %w", err)
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return nil, errors.New("paypal: client_id/client_secret required")
	}
	cfg := Config{}
	if len(confRaw) > 0 {
		_ = json.Unmarshal(confRaw, &cfg)
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api-m.sandbox.paypal.com"
		if c.Mode == "live" {
			cfg.APIBase = "https://api-m.paypal.com"
		}
	}
	return &Provider{creds: c, cfg: cfg}, nil
}

type Provider struct {
	creds Credentials
	cfg   Config

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

func (p *Provider) Name() string { return Name }

// ----- auth -----

func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.tokenExp.Add(-60*time.Second)) {
		return p.token, nil
	}
	creds := base64.StdEncoding.EncodeToString([]byte(p.creds.ClientID + ":" + p.creds.ClientSecret))
	headers := map[string]string{"Authorization": "Basic " + creds}
	form := map[string]string{"grant_type": "client_credentials"}
	var resp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := postForm(ctx, p.cfg.APIBase+"/v1/oauth2/token", form, headers, &resp); err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", errors.New("paypal: empty access token")
	}
	p.token = resp.AccessToken
	p.tokenExp = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	return p.token, nil
}

func (p *Provider) authHeaders(ctx context.Context) (map[string]string, error) {
	t, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + t}, nil
}

// ----- CreateCheckout / Capture / Refund -----

func (p *Provider) CreateCheckout(ctx context.Context, in adapters.CheckoutRequest) (adapters.Checkout, error) {
	switch in.Mode {
	case adapters.ModeFixed, adapters.ModeDeposit, adapters.ModeOpen:
		// one-time payment path below
	case adapters.ModeSubscription:
		return p.createSubscription(ctx, in)
	default:
		return adapters.Checkout{}, fmt.Errorf("paypal: mode %q not supported", in.Mode)
	}
	headers, err := p.authHeaders(ctx)
	if err != nil {
		return adapters.Checkout{}, err
	}
	headers["Prefer"] = "return=representation"
	body := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{{
			"reference_id": in.BookingID,
			"description":  in.Description,
			"amount": map[string]any{
				"currency_code": in.Currency,
				"value":         formatCurrency(in.AmountCents),
			},
		}},
		"application_context": map[string]any{
			"return_url":  in.SuccessURL,
			"cancel_url":  in.CancelURL,
			"user_action": "PAY_NOW",
		},
	}
	var resp struct {
		ID    string `json:"id"`
		Links []struct {
			HREF string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := httpx.DoJSON(ctx, "POST", p.cfg.APIBase+"/v2/checkout/orders", headers, body, &resp); err != nil {
		return adapters.Checkout{}, err
	}
	approve := ""
	for _, l := range resp.Links {
		if l.Rel == "approve" || l.Rel == "payer-action" {
			approve = l.HREF
			break
		}
	}
	if approve == "" {
		return adapters.Checkout{}, errors.New("paypal: no approve link in response")
	}
	return adapters.Checkout{RedirectURL: approve, ExternalID: resp.ID}, nil
}

// createSubscription is the v0.3 PayPal Subscriptions path. Requires the
// event_type.stripe_price_id field to hold a PayPal billing-plan id
// (P-...). Returns the approval link the invitee redirects to.
func (p *Provider) createSubscription(ctx context.Context, in adapters.CheckoutRequest) (adapters.Checkout, error) {
	if in.StripePriceID == "" {
		return adapters.Checkout{}, fmt.Errorf("paypal subscription: plan id required (event_type.stripe_price_id reused as PayPal plan id)")
	}
	headers, err := p.authHeaders(ctx)
	if err != nil {
		return adapters.Checkout{}, err
	}
	headers["Prefer"] = "return=representation"
	body := map[string]any{
		"plan_id":    in.StripePriceID, // PayPal plan id, e.g. P-1AB23...
		"custom_id":  in.BookingID,     // so the webhook can find the booking
		"subscriber": map[string]any{"email_address": in.InviteeMail},
		"application_context": map[string]any{
			"return_url":          in.SuccessURL,
			"cancel_url":          in.CancelURL,
			"user_action":         "SUBSCRIBE_NOW",
			"payment_method":      map[string]any{"payer_selected": "PAYPAL"},
			"shipping_preference": "NO_SHIPPING",
		},
	}
	var resp struct {
		ID    string `json:"id"`
		Links []struct {
			HREF string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := httpx.DoJSON(ctx, "POST",
		p.cfg.APIBase+"/v1/billing/subscriptions", headers, body, &resp); err != nil {
		return adapters.Checkout{}, err
	}
	approve := ""
	for _, l := range resp.Links {
		if l.Rel == "approve" {
			approve = l.HREF
			break
		}
	}
	if approve == "" {
		return adapters.Checkout{}, fmt.Errorf("paypal subscription: no approve link")
	}
	return adapters.Checkout{RedirectURL: approve, ExternalID: resp.ID}, nil
}

// CaptureOrder is PayPal-specific; called by the inbound webhook handler once
// the user has approved the order. Returns the captured payment ID.
func (p *Provider) CaptureOrder(ctx context.Context, orderID string) (string, error) {
	headers, err := p.authHeaders(ctx)
	if err != nil {
		return "", err
	}
	var resp struct {
		ID            string `json:"id"`
		PurchaseUnits []struct {
			Payments struct {
				Captures []struct {
					ID string `json:"id"`
				} `json:"captures"`
			} `json:"payments"`
		} `json:"purchase_units"`
	}
	if err := httpx.DoJSON(ctx, "POST", p.cfg.APIBase+"/v2/checkout/orders/"+orderID+"/capture", headers, map[string]any{}, &resp); err != nil {
		return "", err
	}
	if len(resp.PurchaseUnits) > 0 && len(resp.PurchaseUnits[0].Payments.Captures) > 0 {
		return resp.PurchaseUnits[0].Payments.Captures[0].ID, nil
	}
	return resp.ID, nil
}

// Capture conforms to PaymentProvider but for PayPal the relevant capture is
// CaptureOrder above; this method is a no-op so the interface compiles.
func (p *Provider) Capture(_ context.Context, _ string, _ int) error { return nil }

func (p *Provider) Refund(ctx context.Context, captureID string, amountCents int, currency string) error {
	headers, err := p.authHeaders(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if amountCents > 0 {
		body["amount"] = map[string]any{
			"currency_code": currency,
			"value":         formatCurrency(amountCents),
		}
	}
	return httpx.DoJSON(ctx, "POST",
		p.cfg.APIBase+"/v2/payments/captures/"+captureID+"/refund", headers, body, nil)
}

// ----- VerifyWebhook -----

// PayPal verifies webhooks via a callback to verify-webhook-signature; this
// is the supported approach (offline verification requires PEM certs that
// rotate). We forward the headers + body to PayPal and trust their verdict.
func (p *Provider) VerifyWebhook(rawBody []byte, headers adapters.WebhookHeaders) (adapters.WebhookEvent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	authHeaders, err := p.authHeaders(ctx)
	if err != nil {
		return adapters.WebhookEvent{}, err
	}
	var event map[string]any
	if err := json.Unmarshal(rawBody, &event); err != nil {
		return adapters.WebhookEvent{}, err
	}
	body := map[string]any{
		"transmission_id":   headers.Get("paypal-transmission-id"),
		"transmission_time": headers.Get("paypal-transmission-time"),
		"cert_url":          headers.Get("paypal-cert-url"),
		"auth_algo":         headers.Get("paypal-auth-algo"),
		"transmission_sig":  headers.Get("paypal-transmission-sig"),
		"webhook_id":        p.creds.WebhookID,
		"webhook_event":     event,
	}
	var verifyResp struct {
		Status string `json:"verification_status"`
	}
	if err := httpx.DoJSON(ctx, "POST",
		p.cfg.APIBase+"/v1/notifications/verify-webhook-signature",
		authHeaders, body, &verifyResp); err != nil {
		return adapters.WebhookEvent{}, err
	}
	if verifyResp.Status != "SUCCESS" {
		return adapters.WebhookEvent{}, fmt.Errorf("paypal: webhook verification failed: %s", verifyResp.Status)
	}
	out := adapters.WebhookEvent{
		EventID: fmt.Sprint(event["id"]),
		Raw:     event,
	}
	if t, ok := event["create_time"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			out.OccurredAt = parsed.UTC()
		}
	}
	resource, _ := event["resource"].(map[string]any)
	switch event["event_type"] {
	// CHECKOUT.ORDER.APPROVED is deliberately NOT a success: approval only
	// means the buyer authorized the order, no money moved yet. Treating it as
	// paid confirmed the booking (meeting + calendar + mails) before any
	// capture (2026-07-16). A real capture must be driven server-side
	// (CaptureOrder) and only PAYMENT.CAPTURE.COMPLETED counts. APPROVED falls
	// through here → ignored (booking stays pending_payment).
	case "PAYMENT.CAPTURE.COMPLETED",
		"BILLING.SUBSCRIPTION.ACTIVATED":
		out.Type = adapters.EventPaymentSucceeded
		if resource != nil {
			out.ExternalID, _ = resource["id"].(string)
			if amt, ok := resource["amount"].(map[string]any); ok {
				if v, ok := amt["value"].(string); ok {
					out.AmountCents = parseCurrency(v)
				}
				out.Currency, _ = amt["currency_code"].(string)
			}
			if pus, ok := resource["purchase_units"].([]any); ok && len(pus) > 0 {
				if pu, ok := pus[0].(map[string]any); ok {
					out.BookingID, _ = pu["reference_id"].(string)
				}
			}
			if v, ok := resource["custom_id"].(string); ok && out.BookingID == "" {
				out.BookingID = v
			}
		}
	case "PAYMENT.CAPTURE.DENIED", "CHECKOUT.ORDER.DECLINED",
		"BILLING.SUBSCRIPTION.PAYMENT.FAILED", "BILLING.SUBSCRIPTION.CANCELLED":
		out.Type = adapters.EventPaymentFailed
		if resource != nil {
			out.ExternalID, _ = resource["id"].(string)
		}
	case "PAYMENT.CAPTURE.REFUNDED":
		out.Type = adapters.EventPaymentRefunded
		if resource != nil {
			out.ExternalID, _ = resource["id"].(string)
		}
	}
	return out, nil
}

// ----- helpers -----

func formatCurrency(cents int) string {
	whole := cents / 100
	rest := cents % 100
	return fmt.Sprintf("%d.%02d", whole, rest)
}

func parseCurrency(s string) int {
	f, _ := strconv.ParseFloat(s, 64)
	return int(f*100 + 0.5)
}

func postForm(ctx context.Context, url string, form map[string]string, headers map[string]string, out any) error {
	// httpx.DoForm doesn't pass headers; inline a thin variant here. Stays
	// small (~30 LOC) and avoids leaking PayPal Basic-auth into httpx.
	return formPostWithHeaders(ctx, url, form, headers, out)
}

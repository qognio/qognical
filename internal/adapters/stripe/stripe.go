// Package stripe talks to Stripe's REST API directly — no stripe-go SDK.
// We support the four payment modes from docs/planning/05:
//
//	fixed        Checkout Session, mode=payment
//	deposit      Checkout Session, mode=payment (partial amount)
//	hold         Checkout Session with payment_intent_data[capture_method]=manual
//	subscription Checkout Session, mode=subscription, with stripe_price_id
//
// VerifyWebhook reproduces Stripe's recommended signature check (HMAC-SHA256
// over "<timestamp>.<raw_body>", compared to the v1 schemes in the
// "Stripe-Signature" header). Idempotency is enforced upstream via the
// webhook_deliveries collection keyed on event_id.
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/adapters/httpx"
)

const Name = "stripe"

// Credentials JSON (instance-scoped; populated from env in main).
type Credentials struct {
	SecretKey     string `json:"secret_key"`
	WebhookSecret string `json:"webhook_secret"`
	APIVersion    string `json:"api_version,omitempty"`
}

type Config struct {
	APIBase string `json:"api_base,omitempty"`
}

func Factory(credsRaw, confRaw json.RawMessage) (adapters.PaymentProvider, error) {
	var c Credentials
	if err := json.Unmarshal(credsRaw, &c); err != nil {
		return nil, fmt.Errorf("stripe creds: %w", err)
	}
	if c.SecretKey == "" {
		return nil, errors.New("stripe: secret_key required")
	}
	// Fail closed: without a webhook secret VerifyWebhook would HMAC with an
	// empty key, so ANYONE could forge a signed `checkout.session.completed`
	// and get a booking marked paid without paying (2026-07-16). Refuse to
	// start Stripe at all rather than accept unauthenticated webhooks.
	if c.WebhookSecret == "" {
		return nil, errors.New("stripe: webhook_secret required (refusing to accept unsigned webhooks)")
	}
	cfg := Config{APIBase: "https://api.stripe.com"}
	if len(confRaw) > 0 {
		_ = json.Unmarshal(confRaw, &cfg)
	}
	return &Provider{creds: c, cfg: cfg}, nil
}

type Provider struct {
	creds Credentials
	cfg   Config
}

func (p *Provider) Name() string { return Name }

// ----- CreateCheckout -----

func (p *Provider) CreateCheckout(ctx context.Context, in adapters.CheckoutRequest) (adapters.Checkout, error) {
	form := url.Values{}
	form.Set("success_url", in.SuccessURL)
	form.Set("cancel_url", in.CancelURL)
	form.Set("client_reference_id", in.BookingID)
	if in.InviteeMail != "" {
		form.Set("customer_email", in.InviteeMail)
	}
	// v0.3 Stripe Connect: optional platform fee in basic-charge mode.
	if in.ApplicationFeeCents > 0 && in.ConnectAccountID != "" {
		form.Set("payment_intent_data[application_fee_amount]",
			strconv.Itoa(in.ApplicationFeeCents))
	}
	switch in.Mode {
	case adapters.ModeFixed, adapters.ModeDeposit, adapters.ModeOpen:
		form.Set("mode", "payment")
		form.Set("line_items[0][quantity]", "1")
		form.Set("line_items[0][price_data][currency]", strings.ToLower(in.Currency))
		form.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(in.AmountCents))
		form.Set("line_items[0][price_data][product_data][name]", in.Description)
	case adapters.ModeHold:
		form.Set("mode", "payment")
		form.Set("line_items[0][quantity]", "1")
		form.Set("line_items[0][price_data][currency]", strings.ToLower(in.Currency))
		form.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(in.AmountCents))
		form.Set("line_items[0][price_data][product_data][name]", in.Description)
		form.Set("payment_intent_data[capture_method]", "manual")
	case adapters.ModeSubscription:
		form.Set("mode", "subscription")
		form.Set("line_items[0][quantity]", "1")
		form.Set("line_items[0][price]", in.StripePriceID)
	default:
		return adapters.Checkout{}, fmt.Errorf("unknown mode %q", in.Mode)
	}

	resp, err := p.postFormWithAccount(ctx, "/v1/checkout/sessions", form, in.ConnectAccountID)
	if err != nil {
		return adapters.Checkout{}, err
	}
	id, _ := resp["id"].(string)
	urlStr, _ := resp["url"].(string)
	return adapters.Checkout{RedirectURL: urlStr, ExternalID: id}, nil
}

// AccountLink creates an onboarding URL the host visits to set up their
// Connect account (Standard or Express). Caller passes the host's
// Stripe account id (acct_xxx) and a return-url.
//
// Standard onboarding API: POST /v1/account_links
func (p *Provider) AccountLink(ctx context.Context, accountID, returnURL, refreshURL string) (string, error) {
	form := url.Values{}
	form.Set("account", accountID)
	form.Set("type", "account_onboarding")
	form.Set("return_url", returnURL)
	form.Set("refresh_url", refreshURL)
	resp, err := p.postForm(ctx, "/v1/account_links", form)
	if err != nil {
		return "", err
	}
	u, _ := resp["url"].(string)
	return u, nil
}

// CreateConnectAccount creates a fresh Standard account that the host can
// then onboard via AccountLink. Returns the account id (acct_...).
func (p *Provider) CreateConnectAccount(ctx context.Context, email, country string) (string, error) {
	form := url.Values{}
	form.Set("type", "standard")
	if email != "" {
		form.Set("email", email)
	}
	if country != "" {
		form.Set("country", country)
	}
	resp, err := p.postForm(ctx, "/v1/accounts", form)
	if err != nil {
		return "", err
	}
	id, _ := resp["id"].(string)
	return id, nil
}

// ----- Refund / Capture -----

func (p *Provider) Refund(ctx context.Context, externalID string, amountCents int, currency string) error {
	form := url.Values{}
	// externalID is the checkout-session id; we need its payment_intent.
	pi, err := p.paymentIntentFromSession(ctx, externalID)
	if err != nil {
		return err
	}
	form.Set("payment_intent", pi)
	if amountCents > 0 {
		form.Set("amount", strconv.Itoa(amountCents))
	}
	_, err = p.postForm(ctx, "/v1/refunds", form)
	return err
}

func (p *Provider) Capture(ctx context.Context, externalID string, amountCents int) error {
	pi, err := p.paymentIntentFromSession(ctx, externalID)
	if err != nil {
		return err
	}
	form := url.Values{}
	if amountCents > 0 {
		form.Set("amount_to_capture", strconv.Itoa(amountCents))
	}
	_, err = p.postForm(ctx, "/v1/payment_intents/"+pi+"/capture", form)
	return err
}

func (p *Provider) paymentIntentFromSession(ctx context.Context, sessionID string) (string, error) {
	headers := map[string]string{"Authorization": "Bearer " + p.creds.SecretKey}
	if p.creds.APIVersion != "" {
		headers["Stripe-Version"] = p.creds.APIVersion
	}
	var resp map[string]any
	if err := httpx.DoJSON(ctx, "GET", p.cfg.APIBase+"/v1/checkout/sessions/"+sessionID, headers, nil, &resp); err != nil {
		return "", err
	}
	pi, _ := resp["payment_intent"].(string)
	if pi == "" {
		return "", errors.New("stripe: session missing payment_intent")
	}
	return pi, nil
}

// ----- VerifyWebhook -----

// VerifyWebhook implements the spec at https://stripe.com/docs/webhooks/signatures:
//
//	signed_payload  = timestamp + "." + raw_body
//	expected_sig    = HMAC-SHA256(webhook_secret, signed_payload)
//	header parts    = "t=<ts>,v1=<sig>,v1=<sig>,..."
//
// We also enforce a 5-minute timestamp window to reject replays.
func (p *Provider) VerifyWebhook(rawBody []byte, headers adapters.WebhookHeaders) (adapters.WebhookEvent, error) {
	sigHeader := headers.Get("Stripe-Signature")
	if sigHeader == "" {
		return adapters.WebhookEvent{}, errors.New("stripe: missing signature header")
	}
	var ts string
	var sigs []string
	for _, part := range strings.Split(sigHeader, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	if ts == "" || len(sigs) == 0 {
		return adapters.WebhookEvent{}, errors.New("stripe: signature header missing t/v1")
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return adapters.WebhookEvent{}, err
	}
	if d := time.Since(time.Unix(tsInt, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return adapters.WebhookEvent{}, fmt.Errorf("stripe: timestamp outside ±5m window (%v)", d)
	}
	mac := hmac.New(sha256.New, []byte(p.creds.WebhookSecret))
	mac.Write([]byte(ts + "." + string(rawBody)))
	expected := hex.EncodeToString(mac.Sum(nil))
	matched := false
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(expected)) {
			matched = true
			break
		}
	}
	if !matched {
		return adapters.WebhookEvent{}, errors.New("stripe: signature mismatch")
	}
	var ev struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Created int64  `json:"created"`
		Data    struct {
			Object map[string]any `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &ev); err != nil {
		return adapters.WebhookEvent{}, err
	}
	out := adapters.WebhookEvent{
		EventID:    ev.ID,
		OccurredAt: time.Unix(ev.Created, 0).UTC(),
		Raw:        ev.Data.Object,
	}
	switch ev.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "invoice.paid":
		out.Type = adapters.EventPaymentSucceeded
		out.ExternalID, _ = ev.Data.Object["id"].(string)
		out.BookingID, _ = ev.Data.Object["client_reference_id"].(string)
		if amt, ok := ev.Data.Object["amount_total"].(float64); ok {
			out.AmountCents = int(amt)
		}
		out.Currency, _ = ev.Data.Object["currency"].(string)
	case "checkout.session.expired":
		out.Type = adapters.EventPaymentExpired
		out.ExternalID, _ = ev.Data.Object["id"].(string)
		out.BookingID, _ = ev.Data.Object["client_reference_id"].(string)
	case "charge.refunded":
		out.Type = adapters.EventPaymentRefunded
		out.ExternalID, _ = ev.Data.Object["id"].(string)
	case "checkout.session.async_payment_failed", "payment_intent.payment_failed":
		out.Type = adapters.EventPaymentFailed
		out.ExternalID, _ = ev.Data.Object["id"].(string)
	default:
		// Unknown type — return the event with empty Type so the caller
		// can ack 200 without acting.
	}
	return out, nil
}

// postForm sends form-encoded body with Stripe auth + optional API version.
func (p *Provider) postForm(ctx context.Context, path string, form url.Values) (map[string]any, error) {
	return p.postFormWithAccount(ctx, path, form, "")
}

// postFormWithAccount is the Connect-aware variant: when accountID is
// non-empty, "Stripe-Account: acct_..." is sent so the call runs against
// the connected account.
func (p *Provider) postFormWithAccount(ctx context.Context, path string, form url.Values, accountID string) (map[string]any, error) {
	headers := map[string]string{
		"Authorization": "Bearer " + p.creds.SecretKey,
	}
	if p.creds.APIVersion != "" {
		headers["Stripe-Version"] = p.creds.APIVersion
	}
	if accountID != "" {
		headers["Stripe-Account"] = accountID
	}
	m := make(map[string]string, len(form))
	for k, vs := range form {
		if len(vs) > 0 {
			m[k] = vs[0]
		}
	}
	var resp map[string]any
	return resp, p.formCall(ctx, path, m, headers, &resp)
}

// formCall is a slim form POST that lets us pass extra headers (Authorization
// + Stripe-Version). httpx.DoForm is generic; this adapter-specific helper
// keeps us in stdlib while supporting the auth.
func (p *Provider) formCall(ctx context.Context, path string, form map[string]string, headers map[string]string, out any) error {
	values := url.Values{}
	for k, v := range form {
		values.Set(k, v)
	}
	req, err := newRequest(ctx, "POST", p.cfg.APIBase+path, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpx.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", adapters.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return adapters.ErrAuth
	}
	if resp.StatusCode >= 500 {
		return adapters.ErrUnavailable
	}
	if resp.StatusCode >= 400 {
		raw, _ := readAll(resp.Body)
		return &httpx.APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

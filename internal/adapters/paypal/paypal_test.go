package paypal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qognio/qognical/internal/adapters"
)

func newProvider(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	creds, _ := json.Marshal(Credentials{ClientID: "c", ClientSecret: "s", WebhookID: "wh"})
	conf, _ := json.Marshal(Config{APIBase: srv.URL})
	p, err := Factory(creds, conf)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

func TestCreateOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/oauth2/token") {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"O-123","links":[{"href":"https://paypal/approve","rel":"approve"}]}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	out, err := p.CreateCheckout(context.Background(), adapters.CheckoutRequest{
		BookingID: "bk1", Mode: adapters.ModeFixed, AmountCents: 12345, Currency: "EUR",
		SuccessURL: "https://x/ok", CancelURL: "https://x/cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ExternalID != "O-123" || out.RedirectURL != "https://paypal/approve" {
		t.Errorf("got %+v", out)
	}
}

func TestWebhookVerifySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/oauth2/token") {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"verification_status":"SUCCESS"}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	body := []byte(`{"id":"evt_1","event_type":"PAYMENT.CAPTURE.COMPLETED","create_time":"2026-06-01T10:00:00Z","resource":{"id":"cap_1","amount":{"currency_code":"EUR","value":"42.00"},"purchase_units":[{"reference_id":"bk1"}]}}`)
	ev, err := p.VerifyWebhook(body, adapters.WebhookHeaders{
		"paypal-transmission-id":   "x",
		"paypal-transmission-time": "y",
		"paypal-cert-url":          "z",
		"paypal-auth-algo":         "a",
		"paypal-transmission-sig":  "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != adapters.EventPaymentSucceeded || ev.AmountCents != 4200 || ev.BookingID != "bk1" {
		t.Errorf("got %+v", ev)
	}
}

func TestWebhookVerifyFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/oauth2/token") {
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"verification_status":"FAILURE"}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	if _, err := p.VerifyWebhook([]byte(`{"id":"e"}`), adapters.WebhookHeaders{}); err == nil {
		t.Fatal("expected verification failure")
	}
}

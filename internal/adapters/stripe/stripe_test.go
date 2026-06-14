package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qognio/qognical/internal/adapters"
)

func newProvider(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	creds, _ := json.Marshal(Credentials{SecretKey: "sk_test", WebhookSecret: "whsec_test"})
	conf, _ := json.Marshal(Config{APIBase: srv.URL})
	p, err := Factory(creds, conf)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

func TestCheckoutFixed(t *testing.T) {
	var lastForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastForm = string(b)
		_, _ = w.Write([]byte(`{"id":"cs_123","url":"https://checkout.stripe.com/c/cs_123"}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	out, err := p.CreateCheckout(context.Background(), adapters.CheckoutRequest{
		BookingID: "bk1", Mode: adapters.ModeFixed, AmountCents: 12000, Currency: "EUR",
		Description: "Beratung", SuccessURL: "http://x/ok", CancelURL: "http://x/cancel",
		InviteeMail: "x@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ExternalID != "cs_123" || !strings.HasPrefix(out.RedirectURL, "https://checkout.") {
		t.Errorf("got %+v", out)
	}
	for _, want := range []string{
		"mode=payment", "client_reference_id=bk1",
		"line_items%5B0%5D%5Bprice_data%5D%5Bunit_amount%5D=12000",
		"customer_email=x%40example.com",
	} {
		if !strings.Contains(lastForm, want) {
			t.Errorf("form missing %s: %s", want, lastForm)
		}
	}
}

func TestCheckoutHoldUsesManualCapture(t *testing.T) {
	var lastForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastForm = string(b)
		_, _ = w.Write([]byte(`{"id":"cs_h","url":"u"}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	_, err := p.CreateCheckout(context.Background(), adapters.CheckoutRequest{
		BookingID: "bk1", Mode: adapters.ModeHold, AmountCents: 8000, Currency: "EUR",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastForm, "payment_intent_data%5Bcapture_method%5D=manual") {
		t.Errorf("hold mode must set capture_method=manual: %s", lastForm)
	}
}

func TestWebhookValidSignature(t *testing.T) {
	p := newProvider(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	body := []byte(`{"id":"evt_1","type":"checkout.session.completed","created":` +
		fmt.Sprint(time.Now().Unix()) + `,"data":{"object":{"id":"cs_x","client_reference_id":"bk1","amount_total":1234,"currency":"eur"}}}`)
	ts := fmt.Sprint(time.Now().Unix())
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write([]byte(ts + "." + string(body)))
	sig := hex.EncodeToString(mac.Sum(nil))
	ev, err := p.VerifyWebhook(body, adapters.WebhookHeaders{
		"Stripe-Signature": "t=" + ts + ",v1=" + sig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != adapters.EventPaymentSucceeded || ev.BookingID != "bk1" || ev.AmountCents != 1234 {
		t.Errorf("got %+v", ev)
	}
}

func TestWebhookRejectsTamper(t *testing.T) {
	p := newProvider(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	body := []byte(`{"id":"evt","type":"checkout.session.completed","created":1,"data":{"object":{}}}`)
	ts := fmt.Sprint(time.Now().Unix())
	if _, err := p.VerifyWebhook(body, adapters.WebhookHeaders{
		"Stripe-Signature": "t=" + ts + ",v1=00",
	}); err == nil {
		t.Fatal("expected signature mismatch")
	}
}

func TestWebhookReplayWindow(t *testing.T) {
	p := newProvider(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	body := []byte(`{}`)
	old := fmt.Sprint(time.Now().Add(-10 * time.Minute).Unix())
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write([]byte(old + "." + string(body)))
	sig := hex.EncodeToString(mac.Sum(nil))
	if _, err := p.VerifyWebhook(body, adapters.WebhookHeaders{
		"Stripe-Signature": "t=" + old + ",v1=" + sig,
	}); err == nil {
		t.Fatal("expected replay rejection")
	}
}

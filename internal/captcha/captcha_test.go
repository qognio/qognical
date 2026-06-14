package captcha

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoopAcceptsAnything(t *testing.T) {
	// parens needed: Go parses `NoopVerifier{}.` after `if` as block-start.
	if err := (NoopVerifier{}).Verify("ctx", "", ""); err != nil {
		t.Errorf("noop must accept: %v", err)
	}
}

func TestNewUnknownProvider(t *testing.T) {
	v := New("foobar", "sec")
	if err := v.Verify("ctx", "tok", ""); err == nil {
		t.Error("unknown provider must fail")
	}
}

func TestHTTPVerifierSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("secret") != "s" || r.FormValue("response") != "good" {
			http.Error(w, `{"success":false,"error-codes":["wrong-secret"]}`, http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	v := &httpVerifier{name: "hcaptcha", endpoint: srv.URL, secret: "s"}
	if err := v.Verify("ctx", "good", "1.2.3.4"); err != nil {
		t.Fatalf("good token rejected: %v", err)
	}
	err := v.Verify("ctx", "bad", "")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("expected rejection error, got %v", err)
	}
}

func TestHTTPVerifierEmptyToken(t *testing.T) {
	v := New("hcaptcha", "s")
	if err := v.Verify("ctx", "", ""); err == nil {
		t.Error("empty token must be rejected without HTTP round-trip")
	}
}

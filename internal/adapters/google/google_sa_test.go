package google

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qognio/qognical/internal/adapters"
)

func saCredsJSON(t *testing.T, key *rsa.PrivateKey) json.RawMessage {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	b, _ := json.Marshal(map[string]string{
		"type":           "service_account",
		"private_key":    pemStr,
		"private_key_id": "kid-123",
		"client_email":   "qognical-cal@proj.iam.gserviceaccount.com",
		"token_uri":      "https://oauth2.googleapis.com/token",
		"calendar_id":    "owner@gmail.com",
	})
	return b
}

func TestServiceAccountFreeBusy(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var gotGrant, gotAssertion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			_ = r.ParseForm()
			gotGrant = r.Form.Get("grant_type")
			gotAssertion = r.Form.Get("assertion")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"sa-tok","expires_in":3600}`))
		case strings.HasSuffix(r.URL.Path, "/freeBusy"):
			if got := r.Header.Get("Authorization"); got != "Bearer sa-tok" {
				t.Errorf("freeBusy Authorization = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"calendars":{"owner@gmail.com":{"busy":[{"start":"2026-06-15T10:00:00Z","end":"2026-06-15T11:00:00Z"}]}}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	conf := json.RawMessage(fmt.Sprintf(`{"oauth_base":%q,"api_base":%q}`, srv.URL, srv.URL))
	prov, err := Factory(saCredsJSON(t, key), conf)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	busy, err := prov.FreeBusy(context.Background(),
		time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("freebusy: %v", err)
	}
	if len(busy) != 1 || !busy[0].Start.Equal(time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected busy: %+v", busy)
	}
	if gotGrant != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Errorf("grant_type = %q", gotGrant)
	}
	// The assertion must be a valid RS256 JWT signed by our key with correct claims.
	parts := strings.Split(gotAssertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion not a 3-part JWT: %q", gotAssertion)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("JWT signature invalid: %v", err)
	}
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != "qognical-cal@proj.iam.gserviceaccount.com" {
		t.Errorf("iss = %v", claims["iss"])
	}
	if claims["scope"] != calendarScope {
		t.Errorf("scope = %v", claims["scope"])
	}
	if claims["aud"] != "https://oauth2.googleapis.com/token" {
		t.Errorf("aud = %v", claims["aud"])
	}
}

func TestServiceAccountCreateEventOmitsAttendees(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"access_token":"sa-tok","expires_in":3600}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"id":"evt-1"}`))
	}))
	defer srv.Close()
	conf := json.RawMessage(fmt.Sprintf(`{"oauth_base":%q,"api_base":%q}`, srv.URL, srv.URL))
	prov, err := Factory(saCredsJSON(t, key), conf)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, err := prov.CreateEvent(context.Background(), adapters.CalendarEvent{
		Summary: "x", StartUTC: time.Now().UTC(), EndUTC: time.Now().Add(time.Hour).UTC(),
		AttendeeMail: "guest@example.com", AttendeeName: "Guest",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok := body["attendees"]; ok {
		t.Errorf("service-account event must NOT contain attendees (403 forbiddenForServiceAccounts), got: %v", body["attendees"])
	}
}

func TestOAuthCreateEventKeepsAttendees(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"access_token":"u-tok","expires_in":3600}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"id":"evt-1"}`))
	}))
	defer srv.Close()
	creds := json.RawMessage(`{"client_id":"c","client_secret":"s","refresh_token":"r","calendar_id":"primary"}`)
	conf := json.RawMessage(fmt.Sprintf(`{"oauth_base":%q,"api_base":%q}`, srv.URL, srv.URL))
	prov, err := Factory(creds, conf)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, err := prov.CreateEvent(context.Background(), adapters.CalendarEvent{
		Summary: "x", StartUTC: time.Now().UTC(), EndUTC: time.Now().Add(time.Hour).UTC(),
		AttendeeMail: "guest@example.com", AttendeeName: "Guest",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok := body["attendees"]; !ok {
		t.Error("OAuth user-flow event must keep attendees")
	}
}

func TestServiceAccountRequiresCalendarID(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	b, _ := json.Marshal(map[string]string{
		"type":         "service_account",
		"private_key":  pemStr,
		"client_email": "x@proj.iam.gserviceaccount.com",
		// no calendar_id
	})
	if _, err := Factory(b, nil); err == nil {
		t.Fatal("expected error when service_account has no calendar_id")
	}
}

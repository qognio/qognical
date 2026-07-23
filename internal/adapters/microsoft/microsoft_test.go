package microsoft

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qognio/qognical/internal/adapters"
)

// newProvider builds a Provider whose token + Graph endpoints point at srv.
func newProvider(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	creds, _ := json.Marshal(Credentials{
		ClientID: "c", ClientSecret: "s", RefreshToken: "r", Tenant: "common",
	})
	conf, _ := json.Marshal(Config{GraphBase: srv.URL, LoginBase: srv.URL})
	p, err := Factory(creds, conf)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

func tokenOK(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
}

func TestFactoryRequiresCoreCreds(t *testing.T) {
	if _, err := Factory([]byte(`{"client_id":"c"}`), nil); err == nil {
		t.Fatal("expected error when refresh_token/client_secret missing")
	}
}

func TestTokenIsCached(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			calls++
			tokenOK(w)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	for i := 0; i < 3; i++ {
		if _, err := p.accessToken(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("token endpoint hit %d times, want 1 (cached)", calls)
	}
}

func TestInvalidGrantMapsToErrAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	_, err := p.accessToken(context.Background())
	if !errors.Is(err, adapters.ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}

// A rotated refresh_token must be surfaced to the persist hook with the full,
// updated credentials JSON (otherwise rotation is lost).
func TestRefreshTokenRotationPersisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600,"refresh_token":"rotated"}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)

	var got Credentials
	var called int
	p.SetOnCredentialChange(func(updated json.RawMessage) {
		called++
		_ = json.Unmarshal(updated, &got)
	})
	if _, err := p.accessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("persist hook called %d times, want 1", called)
	}
	if got.RefreshToken != "rotated" {
		t.Fatalf("persisted refresh_token = %q, want rotated", got.RefreshToken)
	}
	if got.ClientID != "c" || got.ClientSecret != "s" {
		t.Fatalf("persisted creds lost client id/secret: %+v", got)
	}
}

// A 2xx create with no id is malformed and must error rather than confirm a
// booking around an unmanageable event.
func TestCreateEventEmptyIDErrors(t *testing.T) {
	var postHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			tokenOK(w)
		case strings.HasSuffix(r.URL.Path, "/events") && r.Method == http.MethodPost:
			postHits++
			_, _ = w.Write([]byte(`{"id":""}`))
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	_, err := p.CreateEvent(context.Background(), adapters.CalendarEvent{
		Summary: "x", StartUTC: time.Now(), EndUTC: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("expected error on empty event id, got nil")
	}
	// Assert the error came from the empty-id path — not some upstream failure
	// (broken token endpoint, wrong route) that would make this test go
	// falsely green.
	if postHits != 1 {
		t.Fatalf("events POST hit %d times, want exactly 1", postHits)
	}
	if !strings.Contains(err.Error(), "returned no id") {
		t.Fatalf("error %q does not mention the empty-id condition", err)
	}
}

// When Teams omits onlineMeeting on create, the provider re-reads the event by
// id to recover the join URL.
func TestCreateEventPollsForJoinURL(t *testing.T) {
	var getByID int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			tokenOK(w)
		case strings.HasSuffix(r.URL.Path, "/events") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"evt1"}`))
		case strings.HasSuffix(r.URL.Path, "/events/evt1") && r.Method == http.MethodGet:
			getByID++
			_, _ = w.Write([]byte(`{"onlineMeeting":{"joinUrl":"https://teams.example/x"}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	got, err := p.CreateEvent(context.Background(), adapters.CalendarEvent{
		Summary: "x", StartUTC: time.Now(), EndUTC: time.Now().Add(time.Hour),
		CreateOnlineMeeting: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExternalID != "evt1" {
		t.Fatalf("ExternalID = %q, want evt1", got.ExternalID)
	}
	if got.MeetingURL != "https://teams.example/x" {
		t.Fatalf("MeetingURL = %q, want the polled join url", got.MeetingURL)
	}
	if getByID != 1 {
		t.Fatalf("event re-read %d times, want 1", getByID)
	}
}

// FreeBusy returns busy windows and drops free / cancelled events.
func TestFreeBusyFiltersFreeAndCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			tokenOK(w)
		case strings.HasSuffix(r.URL.Path, "/calendarView"):
			_, _ = w.Write([]byte(`{"value":[
				{"start":{"dateTime":"2026-06-01T09:00:00.0000000"},"end":{"dateTime":"2026-06-01T10:00:00.0000000"},"showAs":"busy","isCancelled":false},
				{"start":{"dateTime":"2026-06-01T11:00:00.0000000"},"end":{"dateTime":"2026-06-01T12:00:00.0000000"},"showAs":"free","isCancelled":false},
				{"start":{"dateTime":"2026-06-01T13:00:00.0000000"},"end":{"dateTime":"2026-06-01T14:00:00.0000000"},"showAs":"busy","isCancelled":true}
			]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	busy, err := p.FreeBusy(context.Background(), from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(busy) != 1 {
		t.Fatalf("got %d busy intervals, want 1 (free + cancelled dropped)", len(busy))
	}
	if !busy[0].Start.Equal(time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected busy start %v", busy[0].Start)
	}
}

// FreeBusy must FAIL (not silently drop) on an unparsable busy time, so the
// fail-closed validation refuses rather than freeing a busy window.
func TestFreeBusyErrorsOnUnparsableTime(t *testing.T) {
	var viewHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			tokenOK(w)
		case strings.HasSuffix(r.URL.Path, "/calendarView"):
			viewHits++
			_, _ = w.Write([]byte(`{"value":[
				{"start":{"dateTime":"not-a-time"},"end":{"dateTime":"2026-06-01T10:00:00"},"showAs":"busy","isCancelled":false}
			]}`))
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	_, err := p.FreeBusy(context.Background(), from, from.Add(24*time.Hour))
	if err == nil {
		t.Fatal("expected error on unparsable busy time, got nil")
	}
	// Confirm the failure came from parsing the calendarView payload, not from
	// an upstream error that never reached the parse path.
	if viewHits != 1 {
		t.Fatalf("calendarView hit %d times, want exactly 1", viewHits)
	}
	if !strings.Contains(err.Error(), "unparsable busy time") {
		t.Fatalf("error %q is not the unparsable-time failure", err)
	}
}

func TestDeleteEventNotFoundIsNil(t *testing.T) {
	var delHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			tokenOK(w)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/events/gone"):
			delHits++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"ErrorItemNotFound"}}`))
		default:
			// A wrong method/path must NOT be able to make this test pass.
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	if err := p.DeleteEvent(context.Background(), "gone"); err != nil {
		t.Fatalf("deleting a missing event must be nil, got %v", err)
	}
	if delHits != 1 {
		t.Fatalf("DELETE /events/gone hit %d times, want exactly 1", delHits)
	}
}

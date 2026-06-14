package google

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

func newProvider(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	creds, _ := json.Marshal(Credentials{
		ClientID: "c", ClientSecret: "s", RefreshToken: "r", CalendarID: "primary",
	})
	conf, _ := json.Marshal(Config{OAuthBase: srv.URL, APIBase: srv.URL})
	p, err := Factory(creds, conf)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

func TestTokenRefresh(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			calls++
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	if _, err := p.accessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.accessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("token refreshed %d times", calls)
	}
}

func TestInvalidGrantMapsToAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	_, err := p.accessToken(context.Background())
	if !errors.Is(err, adapters.ErrAuth) {
		t.Errorf("expected ErrAuth, got %v", err)
	}
}

func TestFreeBusyParsesIntervals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"calendars":{"primary":{"busy":[{"start":"2026-06-01T07:00:00Z","end":"2026-06-01T08:00:00Z"}]}}}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	from, _ := time.Parse(time.RFC3339, "2026-06-01T00:00:00Z")
	to, _ := time.Parse(time.RFC3339, "2026-06-02T00:00:00Z")
	got, err := p.FreeBusy(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Start.Hour() != 7 || got[0].End.Hour() != 8 {
		t.Errorf("got %+v", got)
	}
}

func TestCreateEventSendsPayloadAndParsesID(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"evt-123"}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	got, err := p.CreateEvent(context.Background(), adapters.CalendarEvent{
		Summary: "Erstgespräch", StartUTC: time.Now().UTC(), EndUTC: time.Now().Add(30 * time.Minute).UTC(),
		AttendeeMail: "klient@example.com", AttendeeName: "Klient",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExternalID != "evt-123" {
		t.Errorf("ExternalID=%q want evt-123", got.ExternalID)
	}
	if gotBody["summary"] != "Erstgespräch" {
		t.Errorf("summary not sent in body: %v", gotBody["summary"])
	}
}

func TestCreateEventWithMeetExtractsJoinURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"evt-9","conferenceData":{"conferenceId":"abc","entryPoints":[{"entryPointType":"phone","uri":"tel:+49"},{"entryPointType":"video","uri":"https://meet.google.com/abc-defg-hij"}]}}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	got, err := p.CreateEvent(context.Background(), adapters.CalendarEvent{
		Summary: "x", CreateOnlineMeeting: true,
		StartUTC: time.Now().UTC(), EndUTC: time.Now().Add(time.Hour).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.MeetingURL != "https://meet.google.com/abc-defg-hij" {
		t.Errorf("MeetingURL=%q want the video entryPoint", got.MeetingURL)
	}
	if got.MeetingID != "abc" {
		t.Errorf("MeetingID=%q want abc", got.MeetingID)
	}
}

func TestUpdateEventUsesPatch(t *testing.T) {
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		method = r.Method
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	if err := p.UpdateEvent(context.Background(), "evt-1", adapters.CalendarEvent{
		Summary: "neu", StartUTC: time.Now().UTC(), EndUTC: time.Now().Add(time.Hour).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if method != "PATCH" {
		t.Errorf("method=%q want PATCH", method)
	}
}

func TestDeleteEventIdempotentOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Not Found"}}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	if err := p.DeleteEvent(context.Background(), "already-gone"); err != nil {
		t.Errorf("delete of missing event must be nil (idempotent), got %v", err)
	}
}

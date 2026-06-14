package msgraph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qognio/qognical/internal/adapters"
)

// mockServer stands in for both login.microsoftonline.com and
// graph.microsoft.com. Path routing distinguishes them.
func mockServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(handler))
}

func newProvider(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	creds, _ := json.Marshal(Credentials{
		TenantID: "t", ClientID: "c", ClientSecret: "s", UserID: "u",
	})
	conf, _ := json.Marshal(Config{GraphBase: srv.URL, LoginBase: srv.URL})
	p, err := Factory(creds, conf)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

func TestTokenCached(t *testing.T) {
	calls := 0
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/oauth2/v2.0/token") {
			calls++
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	defer srv.Close()
	p := newProvider(t, srv)
	if _, err := p.accessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.accessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("token fetched %d times, want 1", calls)
	}
}

func TestCreateEventEmitsTeamsFlags(t *testing.T) {
	var lastBody map[string]any
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/oauth2/v2.0/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		_, _ = w.Write([]byte(`{"id":"evt_42","onlineMeeting":{"joinUrl":"https://teams.example/x"}}`))
	})
	defer srv.Close()
	p := newProvider(t, srv)
	out, err := p.CreateEvent(context.Background(), adapters.CalendarEvent{
		Summary: "Test", CreateOnlineMeeting: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ExternalID != "evt_42" || out.MeetingURL != "https://teams.example/x" {
		t.Errorf("got %+v", out)
	}
	if lastBody["isOnlineMeeting"] != true {
		t.Errorf("isOnlineMeeting missing/false in body: %+v", lastBody)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/oauth2/v2.0/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		http.Error(w, `{"error":"NotFound"}`, http.StatusNotFound)
	})
	defer srv.Close()
	p := newProvider(t, srv)
	if err := p.DeleteEvent(context.Background(), "gone"); err != nil {
		t.Errorf("delete on 404 should be nil, got %v", err)
	}
}

func TestUnavailableMapsToError(t *testing.T) {
	srv := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/oauth2/v2.0/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		http.Error(w, "boom", http.StatusBadGateway)
	})
	defer srv.Close()
	p := newProvider(t, srv)
	_, err := p.CreateEvent(context.Background(), adapters.CalendarEvent{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("expected ErrUnavailable, got %v", err)
	}
}

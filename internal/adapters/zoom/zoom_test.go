package zoom

import (
	"context"
	"encoding/json"
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
		AccountID: "acc", ClientID: "c", ClientSecret: "s", UserID: "host@example.com",
	})
	conf, _ := json.Marshal(Config{OAuthBase: srv.URL, APIBase: srv.URL})
	p, err := Factory(creds, conf)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

func TestCreateMeeting(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":987654321,"join_url":"https://zoom.us/j/987654321?pwd=x"}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	out, err := p.CreateMeeting(context.Background(), adapters.MeetingRequest{
		Summary:  "Sales call",
		StartUTC: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 6, 1, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.JoinURL != "https://zoom.us/j/987654321?pwd=x" || out.ExternalID != "987654321" {
		t.Errorf("got %+v", out)
	}
	if got["duration"] != float64(30) || got["topic"] != "Sales call" {
		t.Errorf("payload off: %v", got)
	}
}

func TestDeleteIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
			return
		}
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	if err := p.DeleteMeeting(context.Background(), "12345"); err != nil {
		t.Errorf("404 should be silently OK: %v", err)
	}
}

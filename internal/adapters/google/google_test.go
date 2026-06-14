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

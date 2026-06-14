package nextcloud

import (
	"context"
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
	creds, _ := json.Marshal(Credentials{
		BaseURL: srv.URL, Username: "alice", AppPassword: "secret", Calendar: "personal",
	})
	p, err := Factory(creds, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

func TestPutCreatesICS(t *testing.T) {
	var got struct {
		method, path, body string
		ifNoneMatch        string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.ifNoneMatch = r.Header.Get("If-None-Match")
		b, _ := io.ReadAll(r.Body)
		got.body = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	p := newProvider(t, srv)

	_, err := p.CreateEvent(context.Background(), adapters.CalendarEvent{
		Summary: "X", AttendeeMail: "x@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "PUT" {
		t.Errorf("method = %s", got.method)
	}
	if !strings.Contains(got.path, "/remote.php/dav/calendars/alice/personal/") {
		t.Errorf("path = %s", got.path)
	}
	if got.ifNoneMatch != "*" {
		t.Errorf("create must use If-None-Match: *, got %q", got.ifNoneMatch)
	}
	if !strings.Contains(got.body, "BEGIN:VEVENT") {
		t.Errorf("body missing VEVENT: %s", got.body)
	}
}

func TestDeleteIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	if err := p.DeleteEvent(context.Background(), "missing"); err != nil {
		t.Errorf("404 must be idempotent: %v", err)
	}
}

func TestFreeBusyParsesVFREEBUSY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `<?xml version="1.0" encoding="utf-8" ?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:propstat>
      <d:prop>
        <cal:calendar-data>FREEBUSY;FBTYPE=BUSY:20260601T070000Z/20260601T073000Z</cal:calendar-data>
      </d:prop>
    </d:propstat>
  </d:response>
</d:multistatus>`)
	}))
	defer srv.Close()
	p := newProvider(t, srv)
	from, _ := time.Parse(time.RFC3339, "2026-06-01T00:00:00Z")
	to, _ := time.Parse(time.RFC3339, "2026-06-02T00:00:00Z")
	got, err := p.FreeBusy(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Start.Hour() != 7 || got[0].End.Minute() != 30 {
		t.Errorf("got %+v", got)
	}
}

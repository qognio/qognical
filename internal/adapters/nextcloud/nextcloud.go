// Package nextcloud implements adapters.CalendarProvider via CalDAV against
// a Nextcloud instance. We rely only on net/http + a tiny VEVENT writer —
// no caldav library — because the subset of operations qognical needs
// (PUT/DELETE one .ics per booking, REPORT for free-busy) is small enough
// to do in <200 lines and easy to audit.
//
// Auth is HTTP Basic with a Nextcloud app password (host-generated, per
// docs/planning/05).
package nextcloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/qognio/qognical/internal/adapters"
	"github.com/qognio/qognical/internal/adapters/httpx"
	"github.com/qognio/qognical/internal/timeutil"
)

const Name = "nextcloud"

// Credentials JSON shape (encrypted at rest).
type Credentials struct {
	BaseURL     string `json:"base_url"` // https://cloud.example.com
	Username    string `json:"username"`
	AppPassword string `json:"app_password"`
	Calendar    string `json:"calendar"` // e.g. "personal"
}

func Factory(credsRaw, _ json.RawMessage) (adapters.CalendarProvider, error) {
	var c Credentials
	if err := json.Unmarshal(credsRaw, &c); err != nil {
		return nil, fmt.Errorf("nextcloud creds: %w", err)
	}
	if c.BaseURL == "" || c.Username == "" || c.AppPassword == "" {
		return nil, errors.New("nextcloud: base_url/username/app_password required")
	}
	if c.Calendar == "" {
		c.Calendar = "personal"
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return &Provider{c: c}, nil
}

type Provider struct{ c Credentials }

func (p *Provider) Name() string { return Name }

func (p *Provider) calendarURL() string {
	return fmt.Sprintf("%s/remote.php/dav/calendars/%s/%s/", p.c.BaseURL, p.c.Username, p.c.Calendar)
}

func (p *Provider) eventURL(externalID string) string {
	return p.calendarURL() + externalID + ".ics"
}

func (p *Provider) auth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(p.c.Username+":"+p.c.AppPassword))
}

// ----- CalendarProvider -----

func (p *Provider) FreeBusy(ctx context.Context, from, to time.Time) ([]timeutil.Interval, error) {
	body := buildFreeBusyReport(from, to)
	req, err := http.NewRequestWithContext(ctx, "REPORT", p.calendarURL(), strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("Depth", "1")
	req.Header.Set("Authorization", p.auth())
	resp, err := httpx.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", adapters.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, adapters.ErrAuth
	}
	if resp.StatusCode >= 500 {
		return nil, adapters.ErrUnavailable
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &httpx.APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	return parseFreeBusyResponse(raw)
}

func (p *Provider) CreateEvent(ctx context.Context, in adapters.CalendarEvent) (adapters.CreatedEvent, error) {
	uid := generateUID()
	ics := buildICS(uid, in, 0)
	if err := p.putICS(ctx, uid, ics, true); err != nil {
		return adapters.CreatedEvent{}, err
	}
	return adapters.CreatedEvent{ExternalID: uid}, nil
}

func (p *Provider) UpdateEvent(ctx context.Context, externalID string, in adapters.CalendarEvent) error {
	ics := buildICS(externalID, in, 1)
	return p.putICS(ctx, externalID, ics, false)
}

func (p *Provider) DeleteEvent(ctx context.Context, externalID string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", p.eventURL(externalID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", p.auth())
	resp, err := httpx.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", adapters.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		return nil // idempotent
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return adapters.ErrAuth
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return &httpx.APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	return nil
}

func (p *Provider) putICS(ctx context.Context, uid, ics string, requireNew bool) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", p.eventURL(uid), strings.NewReader(ics))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	req.Header.Set("Authorization", p.auth())
	if requireNew {
		req.Header.Set("If-None-Match", "*")
	}
	resp, err := httpx.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", adapters.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return adapters.ErrAuth
	}
	if resp.StatusCode >= 500 {
		return adapters.ErrUnavailable
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return &httpx.APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	return nil
}

// ----- ICS / XML helpers -----

func buildICS(uid string, in adapters.CalendarEvent, sequence int) string {
	now := time.Now().UTC().Format("20060102T150405Z")
	var b strings.Builder
	fmt.Fprintln(&b, "BEGIN:VCALENDAR")
	fmt.Fprintln(&b, "VERSION:2.0")
	fmt.Fprintln(&b, "PRODID:-//Qognical//Booking//EN")
	fmt.Fprintln(&b, "CALSCALE:GREGORIAN")
	fmt.Fprintln(&b, "BEGIN:VEVENT")
	fmt.Fprintf(&b, "UID:%s\n", uid)
	fmt.Fprintf(&b, "SEQUENCE:%d\n", sequence)
	fmt.Fprintf(&b, "DTSTAMP:%s\n", now)
	fmt.Fprintf(&b, "DTSTART:%s\n", in.StartUTC.Format("20060102T150405Z"))
	fmt.Fprintf(&b, "DTEND:%s\n", in.EndUTC.Format("20060102T150405Z"))
	fmt.Fprintf(&b, "SUMMARY:%s\n", icsEscape(in.Summary))
	if in.Description != "" {
		fmt.Fprintf(&b, "DESCRIPTION:%s\n", icsEscape(in.Description))
	}
	if in.Location != "" {
		fmt.Fprintf(&b, "LOCATION:%s\n", icsEscape(in.Location))
	}
	if in.OrganizerMail != "" {
		fmt.Fprintf(&b, "ORGANIZER:mailto:%s\n", in.OrganizerMail)
	}
	if in.AttendeeMail != "" {
		fmt.Fprintf(&b, "ATTENDEE;RSVP=FALSE:mailto:%s\n", in.AttendeeMail)
	}
	fmt.Fprintln(&b, "STATUS:CONFIRMED")
	fmt.Fprintln(&b, "END:VEVENT")
	fmt.Fprint(&b, "END:VCALENDAR\n")
	return b.String()
}

func icsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func generateUID() string {
	return fmt.Sprintf("qognical-%d", time.Now().UnixNano())
}

func buildFreeBusyReport(from, to time.Time) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8" ?>
<C:free-busy-query xmlns:C="urn:ietf:params:xml:ns:caldav">
  <C:time-range start="%s" end="%s"/>
</C:free-busy-query>`,
		from.UTC().Format("20060102T150405Z"),
		to.UTC().Format("20060102T150405Z"),
	)
}

// parseFreeBusyResponse pulls FREEBUSY lines out of the multistatus reply.
// The XML envelope wraps a calendar-data property with VFREEBUSY contents.
func parseFreeBusyResponse(raw []byte) ([]timeutil.Interval, error) {
	var ms struct {
		XMLName  xml.Name `xml:"DAV: multistatus"`
		Response []struct {
			Propstat struct {
				Prop struct {
					CalendarData string `xml:"calendar-data"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	// Some servers return the VFREEBUSY block in a different envelope; we
	// fall back to a flat regex-like scan over the whole document if XML
	// parsing didn't produce calendar-data.
	_ = xml.Unmarshal(raw, &ms)
	combined := ""
	for _, r := range ms.Response {
		combined += r.Propstat.Prop.CalendarData
	}
	if combined == "" {
		combined = string(raw)
	}
	return parseVFreeBusy(combined), nil
}

// parseVFreeBusy returns intervals from one or more "FREEBUSY:start/end" lines.
func parseVFreeBusy(s string) []timeutil.Interval {
	out := []timeutil.Interval{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		// "FREEBUSY[;FBTYPE=BUSY]:20260601T070000Z/20260601T073000Z"
		if !strings.HasPrefix(line, "FREEBUSY") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		for _, rng := range strings.Split(line[idx+1:], ",") {
			parts := strings.SplitN(rng, "/", 2)
			if len(parts) != 2 {
				continue
			}
			s, errS := time.Parse("20060102T150405Z", parts[0])
			e, errE := time.Parse("20060102T150405Z", parts[1])
			if errS != nil || errE != nil {
				continue
			}
			out = append(out, timeutil.Interval{Start: s, End: e})
		}
	}
	return out
}

// Package cron registers periodic jobs against PocketBase's built-in cron:
//   - Reminder dispatcher: looks ahead 25h and 65min for confirmed bookings
//     and sends 24h/1h reminder emails.
//   - Cleanup: abandons stuck pending_payment / processing bookings after
//     a configurable timeout (Doc 09 OPS-5).
package cron

import (
	"log/slog"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/qognio/qognical/internal/notifier"
	"github.com/qognio/qognical/internal/pipeline"
	"github.com/qognio/qognical/internal/state"
	"github.com/qognio/qognical/internal/store"
	"github.com/qognio/qognical/migrations"
)

// Register installs the cron jobs. Safe to call once at startup.
func Register(app core.App, repo *store.Repo, pipe *pipeline.Pipeline, n *notifier.Notifier, baseURL string) {
	c := app.Cron()
	if c == nil {
		return
	}
	// Reminder dispatcher every 5 minutes — enough resolution for 1h/24h
	// reminders without spamming the DB.
	c.MustAdd("reminders", "*/5 * * * *", func() {
		dispatchReminders(app, repo, n, baseURL)
	})
	// Cleanup abandoned bookings every 10 minutes.
	c.MustAdd("cleanup-stuck", "*/10 * * * *", func() {
		cleanupStuck(app)
	})
}

// dispatchReminders sends 24h and 1h reminder emails. We use a tiny window
// (5min) around the target time to coincide with the cron interval; that
// avoids double-sends without persisting a "reminder_sent" flag per kind
// (which would require a schema change we'd rather defer).
func dispatchReminders(app core.App, repo *store.Repo, n *notifier.Notifier, baseURL string) {
	now := time.Now().UTC()
	for _, kind := range []struct {
		ahead  time.Duration
		window time.Duration
		label  string
	}{
		{24 * time.Hour, 5 * time.Minute, "24h"},
		{time.Hour, 5 * time.Minute, "1h"},
	} {
		target := now.Add(kind.ahead)
		recs, err := app.FindAllRecords(migrations.CollBookings,
			dbx.NewExp("status = 'confirmed'"),
			dbx.NewExp("start_utc >= {:lo} AND start_utc < {:hi}",
				dbx.Params{"lo": target, "hi": target.Add(kind.window)}),
		)
		if err != nil {
			slog.Warn("reminders: query failed", "kind", kind.label, "err", err)
			continue
		}
		for _, rec := range recs {
			b, _ := repo.FindBookingByID(rec.Id)
			et, _ := repo.FindEventTypeByID(b.EventTypeID)
			host, _ := repo.FindHostByID(b.HostID)
			err := n.SendReminder(notifier.Booking{
				ID:             b.ID,
				EventTypeTitle: et.Title,
				HostName:       host.Name,
				HostEmail:      host.Email,
				InviteeName:    b.InviteeName,
				InviteeEmail:   b.InviteeEmail,
				StartUTC:       b.StartUTC,
				EndUTC:         b.EndUTC,
				InviteeTZ:      b.InviteeTimezone,
				MeetingURL:     b.MeetingJoinURL,
				BaseURL:        baseURL,
			}, kind.label)
			if err != nil {
				slog.Warn("reminders: send failed", "booking", b.ID, "kind", kind.label, "err", err)
			}
		}
	}
}

// cleanupStuck moves pending_payment / processing bookings older than 30
// minutes to "abandoned". 30 minutes matches the Stripe Checkout session
// expiry and the planning-doc default.
func cleanupStuck(app core.App) {
	cutoff := time.Now().UTC().Add(-30 * time.Minute)
	recs, err := app.FindAllRecords(migrations.CollBookings,
		dbx.NewExp("status IN ('pending_payment','processing')"),
		dbx.NewExp("created < {:c}", dbx.Params{"c": cutoff}),
	)
	if err != nil {
		slog.Warn("cleanup: query failed", "err", err)
		return
	}
	for _, rec := range recs {
		from := state.Status(rec.GetString("status"))
		if err := state.Transition(from, state.StatusAbandoned); err != nil {
			continue
		}
		rec.Set("status", string(state.StatusAbandoned))
		if err := app.Save(rec); err != nil {
			slog.Warn("cleanup: save failed", "id", rec.Id, "err", err)
		}
	}
}

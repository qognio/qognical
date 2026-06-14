// Package dsgvo implements the GDPR-mandated export and deletion (anonymisation)
// for invitee personal data. Both run as CLI subcommands (see cmd/qognical).
// The operations are scoped by invitee email so a single subject's data can
// be served across all their bookings without a real user-account model.
package dsgvo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/spf13/cobra"

	"github.com/qognio/qognical/migrations"
)

// ExportCmd registers `qognical export-data --email=...`.
func ExportCmd(app core.App) *cobra.Command {
	var email, outPath string
	cmd := &cobra.Command{
		Use:   "export-data",
		Short: "Export all data for a given invitee email (DSGVO Art. 15)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if email == "" {
				return errors.New("--email is required")
			}
			doc, err := buildExport(app, email)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(doc, "", "  ")
			if outPath == "" {
				_, _ = os.Stdout.Write(b)
				_, _ = os.Stdout.Write([]byte("\n"))
				return nil
			}
			return os.WriteFile(outPath, b, 0o600)
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "Invitee email")
	cmd.Flags().StringVar(&outPath, "out", "", "Output file (default stdout)")
	return cmd
}

// DeleteCmd registers `qognical delete-data --email=...`.
func DeleteCmd(app core.App) *cobra.Command {
	var email string
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete-data",
		Short: "Anonymise all data for a given invitee email (DSGVO Art. 17)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if email == "" {
				return errors.New("--email is required")
			}
			if !yes {
				return errors.New("refusing without --yes (this anonymises records irreversibly)")
			}
			return anonymise(app, email)
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "Invitee email")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm destructive operation")
	return cmd
}

// exportDoc is the JSON shape written to stdout / file.
type exportDoc struct {
	Subject     string          `json:"subject"`
	ExportedAt  time.Time       `json:"exported_at"`
	Bookings    []bookingRecord `json:"bookings"`
	Source      string          `json:"source"`
	GeneratorBy string          `json:"generated_by"`
}

type bookingRecord struct {
	ID                  string         `json:"id"`
	EventTypeID         string         `json:"event_type_id"`
	HostID              string         `json:"host_id"`
	StartUTC            time.Time      `json:"start_utc"`
	EndUTC              time.Time      `json:"end_utc"`
	InviteeName         string         `json:"invitee_name"`
	InviteeEmail        string         `json:"invitee_email"`
	InviteePhone        string         `json:"invitee_phone,omitempty"`
	InviteeTimezone     string         `json:"invitee_timezone"`
	Status              string         `json:"status"`
	IntakeData          map[string]any `json:"intake_data,omitempty"`
	IntakeSchemaVersion int            `json:"intake_schema_version"`
	PaymentStatus       string         `json:"payment_status"`
	CreatedAt           time.Time      `json:"created_at"`
}

func buildExport(app core.App, email string) (exportDoc, error) {
	recs, err := app.FindAllRecords(migrations.CollBookings,
		dbx.HashExp{"invitee_email": email})
	if err != nil {
		return exportDoc{}, fmt.Errorf("query bookings: %w", err)
	}
	out := exportDoc{
		Subject:     email,
		ExportedAt:  time.Now().UTC(),
		Source:      "qognical",
		GeneratorBy: "qognical CLI",
	}
	for _, r := range recs {
		entry := bookingRecord{
			ID:                  r.Id,
			EventTypeID:         r.GetString("event_type"),
			HostID:              r.GetString("host"),
			StartUTC:            r.GetDateTime("start_utc").Time(),
			EndUTC:              r.GetDateTime("end_utc").Time(),
			InviteeName:         r.GetString("invitee_name"),
			InviteeEmail:        r.GetString("invitee_email"),
			InviteePhone:        r.GetString("invitee_phone"),
			InviteeTimezone:     r.GetString("invitee_timezone"),
			Status:              r.GetString("status"),
			IntakeSchemaVersion: r.GetInt("intake_schema_version"),
			PaymentStatus:       r.GetString("payment_status"),
			CreatedAt:           r.GetDateTime("created").Time(),
		}
		if raw := r.GetString("intake_data"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &entry.IntakeData)
		}
		out.Bookings = append(out.Bookings, entry)
	}
	return out, nil
}

// anonymise empties identifying fields while keeping IDs + start/end (for
// statistical retention, per planning-doc Doc 07).
func anonymise(app core.App, email string) error {
	recs, err := app.FindAllRecords(migrations.CollBookings,
		dbx.HashExp{"invitee_email": email})
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("no bookings for %s", email)
	}
	return app.RunInTransaction(func(txApp core.App) error {
		for _, r := range recs {
			r.Set("invitee_name", "anonymised")
			r.Set("invitee_email", "anonymised@invalid.local")
			r.Set("invitee_phone", "")
			r.Set("intake_data", nil)
			r.Set("cancellation_reason", "")
			if err := txApp.Save(r); err != nil {
				return err
			}
		}
		auditColl, err := txApp.FindCollectionByNameOrId(migrations.CollAuditLog)
		if err != nil {
			return err
		}
		audit := core.NewRecord(auditColl)
		audit.Set("actor", "cli")
		audit.Set("action", "dsgvo.delete")
		audit.Set("target_type", "invitee_email")
		audit.Set("target_id", email)
		audit.Set("metadata", map[string]any{"affected": len(recs)})
		return txApp.Save(audit)
	})
}

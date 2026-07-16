// Package migrations holds versioned schema migrations registered with
// PocketBase's migratecmd plugin. Each file's init() calls m.Register(up, down).
//
// Migration 1730000000 creates the v1.0 schema per docs/planning/04 and
// the service_tokens collection per ADR-0002. Single initial migration is
// intentional — the schema is one feature; subsequent migrations evolve it.
package migrations

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(up1730000000, down1730000000)
}

// collection names (stable; used in code, app.FindCollectionByNameOrId).
const (
	CollUsers             = "users"
	CollEventTypes        = "event_types"
	CollAvailability      = "availability"
	CollDateOverrides     = "date_overrides"
	CollBookings          = "bookings"
	CollIntegrations      = "integrations"
	CollServiceTokens     = "service_tokens"
	CollNotificationsLog  = "notifications_log"
	CollOutboundWebhooks  = "outbound_webhooks"
	CollWebhookDeliveries = "webhook_deliveries"
	CollAuditLog          = "audit_log"
)

func up1730000000(app core.App) error {
	steps := []func(core.App) error{
		createUsers,
		createEventTypes,
		createAvailability,
		createDateOverrides,
		createBookings,
		createIntegrations,
		createServiceTokens,
		createNotificationsLog,
		createOutboundWebhooks,
		createWebhookDeliveries,
		createAuditLog,
	}
	for _, step := range steps {
		if err := step(app); err != nil {
			return err
		}
	}
	return nil
}

func down1730000000(app core.App) error {
	// Drop in reverse dependency order. The users collection is system-
	// owned and only had fields appended — we don't delete it, just remove
	// the fields we added.
	names := []string{
		CollAuditLog, CollWebhookDeliveries, CollOutboundWebhooks,
		CollNotificationsLog, CollServiceTokens, CollIntegrations,
		CollBookings, CollDateOverrides, CollAvailability,
		CollEventTypes,
	}
	for _, n := range names {
		c, err := app.FindCollectionByNameOrId(n)
		if err != nil {
			continue
		}
		if err := app.Delete(c); err != nil {
			return fmt.Errorf("drop %s: %w", n, err)
		}
	}
	if users, err := app.FindCollectionByNameOrId(CollUsers); err == nil {
		users.Fields.RemoveByName("timezone")
		users.Fields.RemoveByName("role")
		users.RemoveIndex("idx_users_role")
		_ = app.Save(users)
	}
	return nil
}

// ---------- helpers ----------

func ptrFloat(v float64) *float64 { return &v }

func relateTo(app core.App, name string) (string, error) {
	c, err := app.FindCollectionByNameOrId(name)
	if err != nil {
		return "", fmt.Errorf("relation target %q: %w", name, err)
	}
	return c.Id, nil
}

func autodate() []core.Field {
	return []core.Field{
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	}
}

// ---------- users (auth collection) ----------
//
// PocketBase ships with a default "users" auth collection (system migration
// 1640988000_init.go). We augment it with the host-specific fields the
// planning docs require (timezone, role). The existing fields (email, name,
// password, verified, etc.) are reused as-is.

func createUsers(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollUsers)
	if err != nil {
		return fmt.Errorf("default users collection not found: %w", err)
	}
	c.Fields.Add(
		&core.TextField{Name: "timezone", Max: 64, Required: true, Pattern: `^[A-Za-z_]+/[A-Za-z_/-]+$`},
		&core.SelectField{Name: "role", Values: []string{"admin", "host"}, MaxSelect: 1, Required: true},
	)
	c.AddIndex("idx_users_role", false, "role", "")
	return app.Save(c)
}

// ---------- event_types ----------

func createEventTypes(app core.App) error {
	userId, err := relateTo(app, CollUsers)
	if err != nil {
		return err
	}
	c := core.NewBaseCollection(CollEventTypes)
	c.Fields.Add(
		&core.RelationField{Name: "owner", CollectionId: userId, MinSelect: 1, MaxSelect: 1, Required: true, CascadeDelete: false},
		&core.TextField{Name: "slug", Max: 80, Required: true, Pattern: `^[a-z0-9](-?[a-z0-9])*$`},
		&core.TextField{Name: "title", Max: 200, Required: true},
		&core.EditorField{Name: "description"},
		&core.NumberField{Name: "duration_min", OnlyInt: true, Min: ptrFloat(5), Max: ptrFloat(1440), Required: true},
		&core.NumberField{Name: "buffer_before_min", OnlyInt: true, Min: ptrFloat(0)},
		&core.NumberField{Name: "buffer_after_min", OnlyInt: true, Min: ptrFloat(0)},
		&core.NumberField{Name: "min_notice_min", OnlyInt: true, Min: ptrFloat(0)},
		&core.NumberField{Name: "max_horizon_days", OnlyInt: true, Min: ptrFloat(1), Max: ptrFloat(365)},
		&core.SelectField{Name: "location_type", Values: []string{"online_jitsi", "online_teams", "online_google_meet", "in_person", "phone", "none"}, MaxSelect: 1, Required: true},
		&core.JSONField{Name: "meeting_config"},
		&core.JSONField{Name: "intake_schema"},
		&core.SelectField{Name: "payment_mode", Values: []string{"none", "fixed", "deposit", "hold", "subscription", "open"}, MaxSelect: 1, Required: true},
		&core.NumberField{Name: "payment_amount", OnlyInt: true, Min: ptrFloat(0)},
		&core.TextField{Name: "payment_currency", Max: 3, Pattern: `^[A-Z]{3}$`},
		&core.SelectField{Name: "payment_provider", Values: []string{"stripe", "paypal"}, MaxSelect: 1},
		&core.TextField{Name: "stripe_price_id", Max: 200},
		&core.NumberField{Name: "schema_version", OnlyInt: true, Min: ptrFloat(1), Required: true},
		&core.BoolField{Name: "active", Required: false},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_event_types_owner_slug", true, "owner, slug", "")
	c.AddIndex("idx_event_types_active", false, "active", "")
	return app.Save(c)
}

// ---------- availability ----------

func createAvailability(app core.App) error {
	userId, err := relateTo(app, CollUsers)
	if err != nil {
		return err
	}
	c := core.NewBaseCollection(CollAvailability)
	c.Fields.Add(
		&core.RelationField{Name: "owner", CollectionId: userId, MinSelect: 1, MaxSelect: 1, Required: true, CascadeDelete: true},
		// NumberField.Required treats 0 as "missing", so we can't mark
		// weekday Required without losing Monday (weekday=0). Min/Max still
		// constrains the range; app code rejects records with weekday<0.
		&core.NumberField{Name: "weekday", OnlyInt: true, Min: ptrFloat(0), Max: ptrFloat(6)},
		&core.TextField{Name: "start", Required: true, Pattern: `^([01]\d|2[0-3]):[0-5]\d$`},
		&core.TextField{Name: "end", Required: true, Pattern: `^([01]\d|2[0-3]):[0-5]\d$`},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_availability_owner_weekday", false, "owner, weekday", "")
	return app.Save(c)
}

// ---------- date_overrides ----------

func createDateOverrides(app core.App) error {
	userId, err := relateTo(app, CollUsers)
	if err != nil {
		return err
	}
	c := core.NewBaseCollection(CollDateOverrides)
	c.Fields.Add(
		&core.RelationField{Name: "owner", CollectionId: userId, MinSelect: 1, MaxSelect: 1, Required: true, CascadeDelete: true},
		&core.DateField{Name: "date", Required: true},
		&core.SelectField{Name: "type", Values: []string{"unavailable", "custom_hours"}, MaxSelect: 1, Required: true},
		&core.TextField{Name: "start", Pattern: `^([01]\d|2[0-3]):[0-5]\d$`},
		&core.TextField{Name: "end", Pattern: `^([01]\d|2[0-3]):[0-5]\d$`},
		&core.TextField{Name: "reason", Max: 500},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_date_overrides_owner_date", true, "owner, date", "")
	return app.Save(c)
}

// ---------- bookings ----------

func createBookings(app core.App) error {
	eventTypeId, err := relateTo(app, CollEventTypes)
	if err != nil {
		return err
	}
	userId, err := relateTo(app, CollUsers)
	if err != nil {
		return err
	}
	c := core.NewBaseCollection(CollBookings)
	c.Fields.Add(
		&core.RelationField{Name: "event_type", CollectionId: eventTypeId, MinSelect: 1, MaxSelect: 1, Required: true, CascadeDelete: false},
		&core.RelationField{Name: "host", CollectionId: userId, MinSelect: 1, MaxSelect: 1, Required: true, CascadeDelete: false},
		&core.DateField{Name: "start_utc", Required: true},
		&core.DateField{Name: "end_utc", Required: true},
		&core.TextField{Name: "invitee_name", Max: 200, Required: true},
		&core.EmailField{Name: "invitee_email", Required: true},
		&core.TextField{Name: "invitee_phone", Max: 32, Pattern: `^\+?[0-9 ()\-]{6,32}$`},
		&core.TextField{Name: "invitee_timezone", Max: 64, Required: true, Pattern: `^[A-Za-z_]+/[A-Za-z_/-]+$`},
		&core.SelectField{Name: "status", Values: []string{"draft", "pending_payment", "processing", "confirmed", "cancelled", "rescheduled", "refunded", "abandoned"}, MaxSelect: 1, Required: true},
		&core.JSONField{Name: "intake_data"},
		&core.NumberField{Name: "intake_schema_version", OnlyInt: true, Min: ptrFloat(1)},
		&core.SelectField{Name: "payment_status", Values: []string{"none", "pending", "processing", "paid", "refunded", "failed", "authorization_expired", "refund_failed"}, MaxSelect: 1, Required: true},
		&core.TextField{Name: "payment_external_id", Max: 200},
		&core.NumberField{Name: "payment_amount_paid", OnlyInt: true, Min: ptrFloat(0)},
		&core.URLField{Name: "meeting_join_url"},
		&core.TextField{Name: "meeting_external_id", Max: 200},
		&core.TextField{Name: "external_calendar_id", Max: 200},
		&core.SelectField{Name: "external_calendar_provider", Values: []string{"msgraph", "nextcloud", "google"}, MaxSelect: 1},
		&core.TextField{Name: "cancel_token_hash", Max: 128},
		&core.DateField{Name: "cancelled_at"},
		&core.TextField{Name: "cancellation_reason", Max: 500},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_bookings_host_start", false, "host, start_utc", "")
	c.AddIndex("idx_bookings_event_type_start", false, "event_type, start_utc", "")
	c.AddIndex("idx_bookings_status", false, "status", "")
	c.AddIndex("idx_bookings_payment_external_id", false, "payment_external_id", "payment_external_id != ''")
	return app.Save(c)
}

// ---------- integrations (verschlüsselte Credentials) ----------

func createIntegrations(app core.App) error {
	userId, err := relateTo(app, CollUsers)
	if err != nil {
		return err
	}
	c := core.NewBaseCollection(CollIntegrations)
	c.Fields.Add(
		&core.RelationField{Name: "owner", CollectionId: userId, MinSelect: 1, MaxSelect: 1, Required: true, CascadeDelete: true},
		&core.SelectField{Name: "provider", Values: []string{"msgraph", "nextcloud", "google", "stripe", "paypal", "jitsi"}, MaxSelect: 1, Required: true},
		&core.TextField{Name: "credentials", Max: 16000, Hidden: true},
		&core.JSONField{Name: "config"},
		&core.BoolField{Name: "sync_enabled"},
		&core.DateField{Name: "last_sync_at"},
		&core.TextField{Name: "last_error", Max: 2000},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_integrations_owner_provider", true, "owner, provider", "")
	return app.Save(c)
}

// ---------- service_tokens (ADR-0002) ----------

func createServiceTokens(app core.App) error {
	userId, err := relateTo(app, CollUsers)
	if err != nil {
		return err
	}
	c := core.NewBaseCollection(CollServiceTokens)
	c.Fields.Add(
		&core.TextField{Name: "name", Max: 100, Required: true},
		&core.TextField{Name: "token_hash", Max: 128, Required: true},
		&core.SelectField{Name: "scopes", Values: []string{"bookings:create", "bookings:read", "bookings:cancel", "bookings:reschedule", "availability:read", "event_types:read"}, MaxSelect: 6, Required: true},
		&core.RelationField{Name: "host_binding", CollectionId: userId, MaxSelect: 1, CascadeDelete: false},
		&core.JSONField{Name: "event_type_allowlist"},
		&core.RelationField{Name: "created_by", CollectionId: userId, MinSelect: 1, MaxSelect: 1, Required: true, CascadeDelete: false},
		&core.DateField{Name: "expires_at"},
		&core.DateField{Name: "last_used_at"},
		&core.DateField{Name: "revoked_at"},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_service_tokens_hash", true, "token_hash", "")
	c.AddIndex("idx_service_tokens_name", true, "name", "")
	return app.Save(c)
}

// ---------- notifications_log ----------

func createNotificationsLog(app core.App) error {
	bookingId, err := relateTo(app, CollBookings)
	if err != nil {
		return err
	}
	c := core.NewBaseCollection(CollNotificationsLog)
	c.Fields.Add(
		&core.RelationField{Name: "booking", CollectionId: bookingId, MaxSelect: 1, CascadeDelete: false},
		&core.SelectField{Name: "channel", Values: []string{"email", "webhook"}, MaxSelect: 1, Required: true},
		&core.TextField{Name: "target", Max: 500, Required: true},
		&core.TextField{Name: "event_type", Max: 100, Required: true},
		&core.SelectField{Name: "status", Values: []string{"sent", "failed", "retrying"}, MaxSelect: 1, Required: true},
		&core.NumberField{Name: "attempts", OnlyInt: true, Min: ptrFloat(0)},
		&core.TextField{Name: "error", Max: 2000},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_notifications_booking", false, "booking", "")
	c.AddIndex("idx_notifications_created", false, "created", "")
	return app.Save(c)
}

// ---------- outbound_webhooks ----------

func createOutboundWebhooks(app core.App) error {
	userId, err := relateTo(app, CollUsers)
	if err != nil {
		return err
	}
	c := core.NewBaseCollection(CollOutboundWebhooks)
	c.Fields.Add(
		&core.RelationField{Name: "owner", CollectionId: userId, MaxSelect: 1, CascadeDelete: true},
		&core.URLField{Name: "url", Required: true},
		&core.TextField{Name: "secret", Max: 4000, Hidden: true},
		&core.JSONField{Name: "events"},
		&core.BoolField{Name: "active"},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_outbound_webhooks_owner", false, "owner", "")
	return app.Save(c)
}

// ---------- webhook_deliveries ----------

func createWebhookDeliveries(app core.App) error {
	whId, err := relateTo(app, CollOutboundWebhooks)
	if err != nil {
		return err
	}
	c := core.NewBaseCollection(CollWebhookDeliveries)
	c.Fields.Add(
		&core.RelationField{Name: "webhook", CollectionId: whId, MinSelect: 1, MaxSelect: 1, Required: true, CascadeDelete: true},
		&core.TextField{Name: "event_id", Max: 200, Required: true},
		&core.JSONField{Name: "payload", MaxSize: 1 << 20},
		&core.SelectField{Name: "status", Values: []string{"pending", "delivered", "failed", "abandoned"}, MaxSelect: 1, Required: true},
		&core.NumberField{Name: "attempts", OnlyInt: true, Min: ptrFloat(0)},
		&core.DateField{Name: "next_retry_at"},
		&core.NumberField{Name: "last_response_status", OnlyInt: true},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_webhook_deliveries_event_id", true, "webhook, event_id", "")
	c.AddIndex("idx_webhook_deliveries_status_retry", false, "status, next_retry_at", "")
	return app.Save(c)
}

// ---------- audit_log (append-only) ----------

func createAuditLog(app core.App) error {
	c := core.NewBaseCollection(CollAuditLog)
	c.Fields.Add(
		&core.TextField{Name: "actor", Max: 200, Required: true},
		&core.TextField{Name: "action", Max: 100, Required: true},
		&core.TextField{Name: "target_type", Max: 100},
		&core.TextField{Name: "target_id", Max: 100},
		&core.JSONField{Name: "metadata"},
		&core.TextField{Name: "ip", Max: 64},
	)
	c.Fields.Add(autodate()...)
	c.AddIndex("idx_audit_log_action_created", false, "action, created", "")
	c.AddIndex("idx_audit_log_target", false, "target_type, target_id", "")
	// Leave ListRule/ViewRule/CreateRule/UpdateRule/DeleteRule = nil: only
	// superusers can touch this collection via API. Inserts happen through
	// internal app code; cron cleanup runs as superuser. Enforces INV-10
	// (append-only audit log).
	return app.Save(c)
}

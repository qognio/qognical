// Migration 1740000000 extends the v1.0 schema for the v1.1 features
// listed in docs/planning/10 Phase 1.1:
//
//   - Round-Robin / Pooled Availability  → event_types.hosts[] + assignment_strategy
//   - Gruppen-Events mit Kapazität       → event_types.capacity
//   - Approval-Workflow                  → event_types.requires_approval +
//     bookings.approval_token_hash +
//     bookings.approved_at / approved_by
//   - Branding pro Event-Type            → brand_color, brand_logo_url, custom_css
//   - i18n light                         → event_types.locale, users.default_locale
//
// All collection mutations are idempotent: we look up the existing column
// before adding to avoid double-apply failures on re-run.
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(up1740000000, down1740000000)
}

func up1740000000(app core.App) error {
	if err := extendEventTypes(app); err != nil {
		return err
	}
	if err := extendBookings(app); err != nil {
		return err
	}
	return extendUsersForLocale(app)
}

func down1740000000(app core.App) error {
	// Drop only the fields we added — non-destructive for v1.0 columns.
	if c, err := app.FindCollectionByNameOrId(CollEventTypes); err == nil {
		for _, name := range []string{
			"hosts", "assignment_strategy", "capacity", "requires_approval",
			"brand_color", "brand_logo_url", "custom_css", "locale",
		} {
			c.Fields.RemoveByName(name)
		}
		_ = app.Save(c)
	}
	if c, err := app.FindCollectionByNameOrId(CollBookings); err == nil {
		for _, name := range []string{
			"approval_token_hash", "approved_at", "approved_by", "declined_at",
			"declined_reason", "group_session_id",
		} {
			c.Fields.RemoveByName(name)
		}
		_ = app.Save(c)
	}
	if c, err := app.FindCollectionByNameOrId(CollUsers); err == nil {
		c.Fields.RemoveByName("default_locale")
		_ = app.Save(c)
	}
	return nil
}

func extendEventTypes(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollEventTypes)
	if err != nil {
		return err
	}
	userId, err := relateTo(app, CollUsers)
	if err != nil {
		return err
	}
	add := func(f core.Field) {
		if c.Fields.GetByName(f.GetName()) == nil {
			c.Fields.Add(f)
		}
	}
	// hosts is a multi-relation. owner stays for backward-compat (treated as
	// host #1 when hosts is empty).
	add(&core.RelationField{
		Name: "hosts", CollectionId: userId, MaxSelect: 50,
	})
	add(&core.SelectField{
		Name:      "assignment_strategy",
		Values:    []string{"single", "round_robin", "least_loaded", "collective"},
		MaxSelect: 1,
	})
	add(&core.NumberField{
		Name: "capacity", OnlyInt: true, Min: ptrFloat(1), Max: ptrFloat(500),
	})
	add(&core.BoolField{Name: "requires_approval"})
	add(&core.TextField{Name: "brand_color", Max: 32,
		Pattern: `^(#[0-9A-Fa-f]{3,8}|[a-z]+)?$`})
	add(&core.URLField{Name: "brand_logo_url"})
	add(&core.JSONField{Name: "custom_css", MaxSize: 8 << 10})
	add(&core.SelectField{
		Name: "locale", Values: []string{"de", "en"}, MaxSelect: 1,
	})
	return app.Save(c)
}

func extendBookings(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollBookings)
	if err != nil {
		return err
	}
	userId, err := relateTo(app, CollUsers)
	if err != nil {
		return err
	}
	add := func(f core.Field) {
		if c.Fields.GetByName(f.GetName()) == nil {
			c.Fields.Add(f)
		}
	}
	add(&core.TextField{Name: "approval_token_hash", Max: 128})
	add(&core.DateField{Name: "approved_at"})
	add(&core.RelationField{
		Name: "approved_by", CollectionId: userId, MaxSelect: 1,
	})
	add(&core.DateField{Name: "declined_at"})
	add(&core.TextField{Name: "declined_reason", Max: 500})
	// group_session_id ties together bookings that share the same slot in
	// group-event mode (capacity > 1). Same string for all attendees.
	add(&core.TextField{Name: "group_session_id", Max: 64})

	// extend status with new value 'pending_approval'.
	if f := c.Fields.GetByName("status"); f != nil {
		if sf, ok := f.(*core.SelectField); ok {
			has := false
			for _, v := range sf.Values {
				if v == "pending_approval" {
					has = true
					break
				}
			}
			if !has {
				sf.Values = append(sf.Values, "pending_approval")
			}
		}
	}
	c.AddIndex("idx_bookings_group_session", false, "group_session_id", "group_session_id != ''")
	return app.Save(c)
}

func extendUsersForLocale(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollUsers)
	if err != nil {
		return err
	}
	if c.Fields.GetByName("default_locale") == nil {
		c.Fields.Add(&core.SelectField{
			Name: "default_locale", Values: []string{"de", "en"}, MaxSelect: 1,
		})
	}
	return app.Save(c)
}

// Migration 1780000000 adds the "microsoft" calendar provider (delegated
// MS-Graph OAuth flow, internal/adapters/microsoft) to the two SelectField
// enums that gate persistence:
//
//   - integrations.provider           — so a host may store microsoft creds
//   - bookings.external_calendar_provider — so the confirm-tail may record
//     which provider created the external event (pipeline writes provider.Name()).
//
// Purely additive: existing rows/values are untouched. Without this, PocketBase
// SelectField validation rejects the value "microsoft" on save and the adapter
// is registered in code but unusable end-to-end.
package migrations

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(up1780000000, down1780000000)
}

// addSelectValue appends value to a SelectField's Values (idempotent) and saves.
func addSelectValue(app core.App, collection, field, value string) error {
	c, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		return err
	}
	f, ok := c.Fields.GetByName(field).(*core.SelectField)
	if !ok {
		// A missing / non-select field means the schema is not what this
		// migration assumes; silently "succeeding" would mark it applied while
		// "microsoft" stays unpersistable. Fail loudly so it is caught.
		return fmt.Errorf("migration 1780000000: %s.%s is not a SelectField", collection, field)
	}
	for _, v := range f.Values {
		if v == value {
			return nil // already present
		}
	}
	f.Values = append(f.Values, value)
	return app.Save(c)
}

// removeSelectValue drops value from a SelectField's Values (idempotent) and saves.
func removeSelectValue(app core.App, collection, field, value string) error {
	c, err := app.FindCollectionByNameOrId(collection)
	if err != nil {
		return err
	}
	f, ok := c.Fields.GetByName(field).(*core.SelectField)
	if !ok {
		return nil
	}
	out := f.Values[:0:0]
	for _, v := range f.Values {
		if v != value {
			out = append(out, v)
		}
	}
	f.Values = out
	return app.Save(c)
}

func up1780000000(app core.App) error {
	if err := addSelectValue(app, CollIntegrations, "provider", "microsoft"); err != nil {
		return err
	}
	return addSelectValue(app, CollBookings, "external_calendar_provider", "microsoft")
}

func down1780000000(app core.App) error {
	if err := removeSelectValue(app, CollIntegrations, "provider", "microsoft"); err != nil {
		return err
	}
	return removeSelectValue(app, CollBookings, "external_calendar_provider", "microsoft")
}

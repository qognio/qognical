// Migration 1750000000 (v0.3) adds the online_zoom value to event_types
// .location_type. Idempotent: skips re-add when already present.
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(up1750000000, down1750000000)
}

func up1750000000(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollEventTypes)
	if err != nil {
		return err
	}
	if f := c.Fields.GetByName("location_type"); f != nil {
		if sf, ok := f.(*core.SelectField); ok {
			has := false
			for _, v := range sf.Values {
				if v == "online_zoom" {
					has = true
					break
				}
			}
			if !has {
				sf.Values = append(sf.Values, "online_zoom")
			}
		}
	}
	return app.Save(c)
}

func down1750000000(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollEventTypes)
	if err != nil {
		return nil
	}
	if f := c.Fields.GetByName("location_type"); f != nil {
		if sf, ok := f.(*core.SelectField); ok {
			out := sf.Values[:0]
			for _, v := range sf.Values {
				if v != "online_zoom" {
					out = append(out, v)
				}
			}
			sf.Values = out
		}
	}
	return app.Save(c)
}

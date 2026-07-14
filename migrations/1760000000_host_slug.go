// Migration 1760000000 gives hosts a proper public URL slug.
//
// Until now the public booking URL used the host's EMAIL as the path segment
// (/book/finn@fmhc.io/{event}) — the address leaked into every booking link
// an embedder put on their site. This adds an optional, unique `slug` on
// users so hosts get clean URLs (/book/qognio/{event}).
//
// The slug is OPTIONAL on purpose: hosts without one keep resolving by email
// (see api.findHostBySlug), so every link minted before this migration stays
// valid. The unique index is therefore partial — it only constrains non-empty
// slugs, letting any number of slug-less hosts coexist.
//
// Idempotent: re-running skips the field when it already exists.
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(up1760000000, down1760000000)
}

func up1760000000(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollUsers)
	if err != nil {
		return err
	}
	if c.Fields.GetByName("slug") == nil {
		c.Fields.Add(&core.TextField{
			Name:    "slug",
			Max:     80,
			Pattern: `^[a-z0-9](-?[a-z0-9])*$`,
		})
	}
	// Unique only among hosts that actually have a slug.
	c.AddIndex("idx_users_slug", true, "slug", "slug != ''")
	return app.Save(c)
}

func down1760000000(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollUsers)
	if err != nil {
		return err
	}
	c.RemoveIndex("idx_users_slug")
	c.Fields.RemoveByName("slug")
	return app.Save(c)
}

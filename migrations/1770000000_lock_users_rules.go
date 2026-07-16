// Migration 1770000000 closes anonymous self-registration on the users
// collection and blocks self-service role/verified escalation.
//
// SECURITY (2026-07-16, Codex review): qognical augments PocketBase's default
// `users` auth collection but inherited its rules unchanged. The default
// CreateRule is "" (PUBLIC) — so `POST /api/collections/users/records` let
// ANYONE create an account with `role=host` outside the custom API, then log
// in and drive /api/host/* (event-types, availability, MS-Graph integrations,
// booking/e-mail flows). Confirmed live-exploitable before this migration.
//
// qognical has no self-signup product flow: hosts are provisioned by a
// superuser (PB admin UI / setup scripts), and superusers bypass collection
// rules — so locking these rules changes nothing for legitimate operation.
//
//   - CreateRule = nil  → only superusers may create users (no public signup).
//   - UpdateRule = nil  → only superusers may write user records. Nothing in
//     the app self-updates the users row via its own bearer (host self-service
//     lives in separate collections); PB's framework auth flows (password
//     reset / verification / email-change confirm) bypass UpdateRule, so login
//     and account recovery keep working. This also removes the role/verified
//     self-escalation vector.
//
// View/Delete rules are intentionally left as the PB defaults
// (id = @request.auth.id): a host may read and delete their own account.
package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(up1770000000, down1770000000)
}

func up1770000000(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollUsers)
	if err != nil {
		return err
	}
	c.CreateRule = nil // superuser-only; no anonymous registration
	c.UpdateRule = nil // superuser-only; blocks self role/verified escalation
	return app.Save(c)
}

func down1770000000(app core.App) error {
	c, err := app.FindCollectionByNameOrId(CollUsers)
	if err != nil {
		return err
	}
	// Restore PocketBase's default users rules.
	public := ""
	own := "id = @request.auth.id"
	c.CreateRule = &public
	c.UpdateRule = &own
	return app.Save(c)
}

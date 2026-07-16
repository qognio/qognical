package api

import (
	"html"
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/qognio/qognical/internal/token"
)

// Approve / decline are two-step: a GET (the email link, which mail-security
// scanners, link-previews and prefetchers auto-fetch) renders a no-store
// confirmation page ONLY; the irreversible state change happens exclusively on
// POST from that page's button. This prevents a scanner from silently
// approving/declining a booking (2026-07-16). The token is echoed into the
// form and posted back in the body, never re-logged as a mutating GET.

func (a *API) handleApprove(e *core.RequestEvent) error {
	return a.handleApprovalAction(e, false)
}

func (a *API) handleDecline(e *core.RequestEvent) error {
	return a.handleApprovalAction(e, true)
}

func (a *API) handleApprovalAction(e *core.RequestEvent, decline bool) error {
	id := e.Request.PathValue("id")
	tok := e.Request.URL.Query().Get("token")
	if tok == "" {
		tok = e.Request.Header.Get("X-Booking-Token")
	}
	if tok == "" && e.Request.Method == http.MethodPost {
		tok = e.Request.FormValue("token") // confirmation-form POST
	}
	// Never let a proxy/browser cache a page that carries the token.
	e.Response.Header().Set("Cache-Control", "no-store")
	e.Response.Header().Set("Referrer-Policy", "no-referrer")

	b, err := a.Repo.FindBookingByID(id)
	if err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "booking not found", nil)
	}
	if b.ApprovalTokenHash == "" {
		return writeErr(e, http.StatusGone, CodeTokenAlreadyUsed, "approval already handled", nil)
	}
	if _, _, err := a.Tokens.Verify(tok, b.ApprovalTokenHash); err != nil {
		return writeErr(e, http.StatusUnauthorized, mapTokenErr(err), err.Error(), nil)
	}

	// GET (incl. mail-scanner prefetch) → confirmation page, NO state change.
	if e.Request.Method == http.MethodGet {
		verb, action := "Buchung bestätigen", "approve"
		if decline {
			verb, action = "Buchung ablehnen", "decline"
		}
		return e.HTML(http.StatusOK, confirmActionHTML(id, action, tok, verb))
	}

	// POST → perform the action.
	if decline {
		reason := e.Request.URL.Query().Get("reason")
		if _, err := a.Pipeline.HandleDecline(id, reason); err != nil {
			return internalErrLog(e, err, "HandleDecline")
		}
		return e.HTML(http.StatusOK, simpleHTML(
			"Anfrage abgelehnt",
			"Die Buchung wurde storniert. Der Invitee wurde benachrichtigt."))
	}
	if _, err := a.Pipeline.HandleApproval(id); err != nil {
		return internalErrLog(e, err, "HandleApproval")
	}
	return e.HTML(http.StatusOK, simpleHTML(
		"Buchung bestätigt",
		"Die Anfrage wurde angenommen. Der Invitee wurde benachrichtigt."))
}

// confirmActionHTML renders the GET confirmation page: a button that POSTs the
// token back to the same path. Token in a hidden field, page marked no-store.
func confirmActionHTML(bookingID, action, tok, verb string) string {
	post := "/api/public/v1/bookings/" + html.EscapeString(bookingID) + "/" + action
	return simpleHTML(verb,
		`Bitte bestätige die Aktion.</p>
<form method="POST" action="`+post+`" style="margin-top:16px">
  <input type="hidden" name="token" value="`+html.EscapeString(tok)+`">
  <button type="submit" style="font:inherit;padding:10px 18px;border:0;border-radius:8px;background:#2563eb;color:#fff;cursor:pointer">`+
			html.EscapeString(verb)+`</button>
</form><p>`)
}

// suppress unused-import lint when token package is only used indirectly
// via Tokens.Verify on the API struct above.
var _ = token.Action("")

// simpleHTML wraps a title + paragraph in a minimal centered card. Used for
// host-facing one-shot pages (approve / decline confirmation).
func simpleHTML(title, body string) string {
	return `<!doctype html><html lang="de"><head><meta charset="utf-8">
<title>` + title + `</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body { font: 16px/1.5 system-ui, sans-serif; background:#f6f7fb;
       display:flex; align-items:center; justify-content:center;
       min-height:100vh; margin:0; padding:24px; }
.card { background:#fff; padding:28px 32px; border-radius:12px;
        box-shadow:0 4px 16px rgba(0,0,0,0.06); max-width:480px; }
h1 { font-size:1.4rem; margin:0 0 8px; }
p { color:#444; margin:0; }
</style></head><body>
<div class="card"><h1>` + title + `</h1><p>` + body + `</p></div>
</body></html>`
}

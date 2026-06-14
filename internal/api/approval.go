package api

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"github.com/qognio/qognical/internal/token"
)

// handleApprove + handleDecline accept GET or POST so the host can click
// the link straight from the email (browser sends GET) or trigger via API.

func (a *API) handleApprove(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	tok := e.Request.URL.Query().Get("token")
	if tok == "" {
		tok = e.Request.Header.Get("X-Booking-Token")
	}
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
	if _, err := a.Pipeline.HandleApproval(id); err != nil {
		return internalErrLog(e, err, "HandleApproval")
	}
	// Respond friendly for the browser flow.
	return e.HTML(http.StatusOK, simpleHTML(
		"Buchung bestätigt",
		"Die Anfrage wurde angenommen. Der Invitee wurde benachrichtigt."))
}

func (a *API) handleDecline(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	tok := e.Request.URL.Query().Get("token")
	if tok == "" {
		tok = e.Request.Header.Get("X-Booking-Token")
	}
	reason := e.Request.URL.Query().Get("reason")
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
	if _, err := a.Pipeline.HandleDecline(id, reason); err != nil {
		return internalErrLog(e, err, "HandleDecline")
	}
	return e.HTML(http.StatusOK, simpleHTML(
		"Anfrage abgelehnt",
		"Die Buchung wurde storniert. Der Invitee wurde benachrichtigt."))
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

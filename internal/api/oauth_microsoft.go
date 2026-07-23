// Hosted Microsoft-365 OAuth flow: a host consents their calendar in the
// browser with one click (no local script). The ONE dedicated Entra app
// (MSOAuthConfig) is server config; each host consents once and we persist
// their rotating refresh_token as a "microsoft" integration row — the exact
// credential shape the microsoft calendar adapter expects.
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/qognio/qognical/internal/adapters/httpx"
	"github.com/qognio/qognical/internal/crypto"
	"github.com/qognio/qognical/internal/store"
	"github.com/qognio/qognical/migrations"
)

const (
	msScope    = "offline_access https://graph.microsoft.com/Calendars.ReadWrite"
	msProvider = "microsoft"
)

// MSOAuth wires the /oauth/microsoft/{start,callback} routes.
type MSOAuth struct {
	App          core.App
	Repo         *store.Repo
	Master       *crypto.Master
	ClientID     string
	ClientSecret string
	Tenant       string
	BaseURL      string
	StateKey     []byte // HMAC key for the stateless signed `state`
}

func (m *MSOAuth) Register(se *core.ServeEvent) {
	se.Router.GET("/oauth/microsoft/start", m.handleStart)
	se.Router.GET("/oauth/microsoft/callback", m.handleCallback)
}

func (m *MSOAuth) configured() bool { return m.ClientID != "" && m.ClientSecret != "" }

func (m *MSOAuth) tenant() string {
	if m.Tenant == "" {
		return "organizations"
	}
	return m.Tenant
}

func (m *MSOAuth) redirectURI() string {
	return strings.TrimRight(m.BaseURL, "/") + "/oauth/microsoft/callback"
}

// signState binds "hostID.expiryUnix" with HMAC so the public callback can
// trust which host consented — no server-side session store, survives restarts.
func (m *MSOAuth) signState(hostID string, exp int64) string {
	payload := hostID + "." + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, m.StateKey)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

func (m *MSOAuth) verifyState(state string) (string, error) {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed state")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("bad state encoding")
	}
	payload := string(raw)
	mac := hmac.New(sha256.New, m.StateKey)
	mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return "", fmt.Errorf("state signature mismatch")
	}
	pp := strings.SplitN(payload, ".", 2)
	if len(pp) != 2 {
		return "", fmt.Errorf("bad state payload")
	}
	exp, err := strconv.ParseInt(pp[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", fmt.Errorf("state expired")
	}
	return pp[0], nil
}

// GET /oauth/microsoft/start?host=<email|slug> — resolves the host, then
// redirects the browser to Microsoft's consent screen.
func (m *MSOAuth) handleStart(e *core.RequestEvent) error {
	if !m.configured() {
		return e.HTML(http.StatusServiceUnavailable, simpleHTML("Nicht verfügbar",
			"Die Microsoft-Anbindung ist auf diesem Server noch nicht konfiguriert."))
	}
	hostQuery := strings.TrimSpace(e.Request.URL.Query().Get("host"))
	if hostQuery == "" {
		return e.HTML(http.StatusBadRequest, simpleHTML("Fehler", "Es fehlt der host-Parameter."))
	}
	host, err := m.Repo.FindHostByEmail(hostQuery)
	if err != nil {
		host, err = m.Repo.FindHostBySlug(hostQuery)
		if err != nil {
			return e.HTML(http.StatusNotFound, simpleHTML("Host nicht gefunden",
				"Zu diesem Zugang gibt es keinen Qognical-Host."))
		}
	}
	state := m.signState(host.ID, time.Now().Add(15*time.Minute).Unix())
	q := url.Values{}
	q.Set("client_id", m.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", m.redirectURI())
	q.Set("response_mode", "query")
	q.Set("scope", msScope)
	q.Set("state", state)
	q.Set("prompt", "select_account")
	authURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/authorize?%s",
		m.tenant(), q.Encode())
	return e.Redirect(http.StatusFound, authURL)
}

// GET /oauth/microsoft/callback?code&state — exchanges the code (client_secret
// stays server-side), encrypts and stores the refresh_token as the host's
// "microsoft" integration.
func (m *MSOAuth) handleCallback(e *core.RequestEvent) error {
	qv := e.Request.URL.Query()
	if oe := qv.Get("error"); oe != "" {
		return e.HTML(http.StatusBadRequest, simpleHTML("Abgebrochen",
			"Microsoft meldete: "+oe+" — "+qv.Get("error_description")))
	}
	code := qv.Get("code")
	state := qv.Get("state")
	if code == "" || state == "" {
		return e.HTML(http.StatusBadRequest, simpleHTML("Fehler", "code oder state fehlt."))
	}
	hostID, err := m.verifyState(state)
	if err != nil {
		return e.HTML(http.StatusBadRequest, simpleHTML("Link ungültig",
			"Dieser Anmelde-Link ist ungültig oder abgelaufen. Bitte starte die Verbindung neu."))
	}

	form := url.Values{}
	form.Set("client_id", m.ClientID)
	form.Set("client_secret", m.ClientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", m.redirectURI())
	form.Set("scope", msScope)
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", m.tenant())

	req, err := http.NewRequestWithContext(e.Request.Context(), http.MethodPost, tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return e.HTML(http.StatusInternalServerError, simpleHTML("Fehler", "Token-Request konnte nicht gebaut werden."))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpx.DefaultClient.Do(req)
	if err != nil {
		return e.HTML(http.StatusBadGateway, simpleHTML("Fehler",
			"Microsoft war nicht erreichbar. Bitte später erneut versuchen."))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tok struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tok)
	if resp.StatusCode != http.StatusOK || tok.RefreshToken == "" {
		detail := tok.ErrorDesc
		if detail == "" {
			detail = string(body)
		}
		return e.HTML(http.StatusBadGateway, simpleHTML("Nicht verbunden",
			"Microsoft hat kein Refresh-Token geliefert. Prüfe, dass 'offline_access' + 'Calendars.ReadWrite' konsentiert wurden. ("+trimLen(detail, 200)+")"))
	}

	creds := map[string]any{
		"client_id":     m.ClientID,
		"client_secret": m.ClientSecret,
		"refresh_token": tok.RefreshToken,
		"tenant":        m.tenant(),
	}
	plain, err := json.Marshal(creds)
	if err != nil {
		return e.HTML(http.StatusInternalServerError, simpleHTML("Fehler", "Interner Fehler."))
	}
	ciphertext, err := m.Master.Encrypt(plain)
	if err != nil {
		return e.HTML(http.StatusInternalServerError, simpleHTML("Fehler", "Verschlüsselung fehlgeschlagen."))
	}

	err = m.App.RunInTransaction(func(txApp core.App) error {
		coll, err := txApp.FindCollectionByNameOrId(migrations.CollIntegrations)
		if err != nil {
			return err
		}
		existing, _ := txApp.FindFirstRecordByFilter(migrations.CollIntegrations,
			"owner = {:owner} && provider = {:provider}",
			dbx.Params{"owner": hostID, "provider": msProvider})
		var rec *core.Record
		if existing != nil {
			rec = existing
		} else {
			rec = core.NewRecord(coll)
			rec.Set("owner", hostID)
			rec.Set("provider", msProvider)
		}
		rec.Set("credentials", ciphertext)
		rec.Set("sync_enabled", true)
		return txApp.Save(rec)
	})
	if err != nil {
		return e.HTML(http.StatusInternalServerError, simpleHTML("Fehler",
			"Konnte die Verbindung nicht speichern: "+err.Error()))
	}

	return e.HTML(http.StatusOK, simpleHTML("Verbunden ✓",
		"Dein Microsoft-365-Kalender ist jetzt mit deiner Terminbuchung verbunden. Du kannst dieses Fenster schließen."))
}

func trimLen(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

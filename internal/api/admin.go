// Admin-API for service-token management (per ADR-0002). Lives under
// /api/admin/v1/... and requires a real superuser session (PocketBase auth).
// We piggy-back on PocketBase's existing admin auth — no second auth system.
package api

import (
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/qognio/qognical/internal/store"
	"github.com/qognio/qognical/internal/svctoken"
)

// RegisterAdmin attaches the /api/admin/v1 group. Wired from main.go alongside
// the public-API group.
func (a *API) RegisterAdmin(se *core.ServeEvent) {
	g := se.Router.Group("/api/admin/v1")
	g.BindFunc(a.requireSuperuser)

	g.POST("/service-tokens", a.handleServiceTokenCreate)
	g.GET("/service-tokens", a.handleServiceTokenList)
	g.POST("/service-tokens/{id}/revoke", a.handleServiceTokenRevoke)
	g.DELETE("/service-tokens/{id}", a.handleServiceTokenDelete)
}

// requireSuperuser blocks the admin group unless the request carries a
// valid PocketBase superuser auth record. PB sets e.Auth on the request
// when its built-in middleware processes the Bearer token.
func (a *API) requireSuperuser(e *core.RequestEvent) error {
	if !e.HasSuperuserAuth() {
		return writeErr(e, http.StatusUnauthorized, CodeTokenInvalid, "superuser auth required", nil)
	}
	return e.Next()
}

type serviceTokenCreateReq struct {
	Name               string   `json:"name"`
	Scopes             []string `json:"scopes"`
	HostBinding        string   `json:"host_binding,omitempty"`
	EventTypeAllowlist []string `json:"event_type_allowlist,omitempty"`
	ExpiresAt          string   `json:"expires_at,omitempty"`
}

type serviceTokenCreateResp struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"` // raw value — shown to user exactly once
}

func (a *API) handleServiceTokenCreate(e *core.RequestEvent) error {
	var req serviceTokenCreateReq
	if err := e.BindBody(&req); err != nil {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "malformed json", nil)
	}
	if req.Name == "" {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "name required", nil)
	}
	if len(req.Scopes) == 0 {
		return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "at least one scope required", nil)
	}
	for _, s := range req.Scopes {
		if !validScope(s) {
			return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "unknown scope: "+s, nil)
		}
	}
	var expiresAt time.Time
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			return writeErr(e, http.StatusBadRequest, CodeInvalidRequest, "expires_at must be RFC3339", nil)
		}
		expiresAt = t.UTC()
	}
	raw, hash, err := svctoken.Generate()
	if err != nil {
		return internalErrLog(e, err, "svctoken.Generate")
	}
	createdBy := ""
	if e.Auth != nil {
		createdBy = e.Auth.Id
	}
	stored, err := a.Repo.CreateServiceToken(req.Name, hash, createdBy, req.Scopes,
		req.HostBinding, req.EventTypeAllowlist, expiresAt)
	if err != nil {
		return internalErrLog(e, err, "CreateServiceToken")
	}
	_ = a.Repo.WriteAudit(store.AuditEntry{
		Actor:      "admin:" + createdBy,
		Action:     "service_token.created",
		TargetType: "service_token",
		TargetID:   stored.ID,
		Metadata:   map[string]any{"name": req.Name, "scopes": req.Scopes},
	})
	return e.JSON(http.StatusCreated, serviceTokenCreateResp{
		ID: stored.ID, Name: stored.Name, Token: raw,
	})
}

func (a *API) handleServiceTokenList(e *core.RequestEvent) error {
	tokens, err := a.Repo.ListServiceTokens()
	if err != nil {
		return internalErrLog(e, err, "ListServiceTokens")
	}
	out := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		m := map[string]any{
			"id": t.ID, "name": t.Name, "scopes": t.Scopes,
			"host_binding":         t.HostBinding,
			"event_type_allowlist": t.EventTypeAllowlist,
			"created_by":           t.CreatedBy,
		}
		if !t.ExpiresAt.IsZero() {
			m["expires_at"] = t.ExpiresAt
		}
		if !t.LastUsedAt.IsZero() {
			m["last_used_at"] = t.LastUsedAt
		}
		if !t.RevokedAt.IsZero() {
			m["revoked_at"] = t.RevokedAt
		}
		out = append(out, m)
	}
	return e.JSON(http.StatusOK, map[string]any{"tokens": out})
}

func (a *API) handleServiceTokenRevoke(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	if err := a.Repo.RevokeServiceToken(id); err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "token not found", nil)
	}
	actor := ""
	if e.Auth != nil {
		actor = e.Auth.Id
	}
	_ = a.Repo.WriteAudit(store.AuditEntry{
		Actor: "admin:" + actor, Action: "service_token.revoked",
		TargetType: "service_token", TargetID: id,
	})
	return e.NoContent(http.StatusNoContent)
}

func (a *API) handleServiceTokenDelete(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")
	if err := a.Repo.DeleteServiceToken(id); err != nil {
		return writeErr(e, http.StatusNotFound, CodeNotFound, "token not found", nil)
	}
	return e.NoContent(http.StatusNoContent)
}

func validScope(s string) bool {
	switch svctoken.Scope(s) {
	case svctoken.ScopeBookingsCreate, svctoken.ScopeBookingsRead,
		svctoken.ScopeBookingsCancel, svctoken.ScopeBookingsReschedule,
		svctoken.ScopeAvailabilityRead, svctoken.ScopeEventTypesRead:
		return true
	}
	return false
}

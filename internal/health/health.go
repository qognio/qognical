// Package health implements the readiness/liveness check used by /healthz
// and by `qognical healthcheck` (Docker HEALTHCHECK).
//
// We check what the binary can reason about locally (DB reachable, schema
// applied) and do not call external provider APIs — those would make the
// healthcheck unstable in ways unrelated to our own service. Provider
// reachability lives in `qognical diagnose-providers`.
package health

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/qognio/qognical/migrations"
)

// expectedCollections lists the collections the schema migration must have
// installed. Missing one signals "schema not applied" → unhealthy.
var expectedCollections = []string{
	migrations.CollUsers,
	migrations.CollEventTypes,
	migrations.CollAvailability,
	migrations.CollDateOverrides,
	migrations.CollBookings,
	migrations.CollIntegrations,
	migrations.CollServiceTokens,
	migrations.CollNotificationsLog,
	migrations.CollOutboundWebhooks,
	migrations.CollWebhookDeliveries,
	migrations.CollAuditLog,
}

// Result is the JSON shape returned by /healthz.
type Result struct {
	Status     string            `json:"status"`
	Version    string            `json:"version"`
	CheckedAt  time.Time         `json:"checked_at"`
	Components map[string]string `json:"components"`
	Errors     []string          `json:"errors,omitempty"`
}

// Run executes all local checks. Returns nil error if healthy.
func Run(app core.App, version string) *Result {
	r := &Result{
		Status:     "ok",
		Version:    version,
		CheckedAt:  time.Now().UTC(),
		Components: map[string]string{},
	}

	// DB ping: any cheap query against _migrations confirms r/w access.
	var n int
	if err := app.DB().NewQuery("SELECT count(*) FROM _migrations").Row(&n); err != nil {
		r.fail("db", err)
	} else {
		r.Components["db"] = "ok"
	}

	// Schema: every expected collection must exist.
	missing := []string{}
	for _, name := range expectedCollections {
		if _, err := app.FindCollectionByNameOrId(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		r.fail("schema", fmt.Errorf("missing collections: %v", missing))
	} else {
		r.Components["schema"] = "ok"
	}

	return r
}

func (r *Result) fail(component string, err error) {
	r.Status = "degraded"
	r.Components[component] = "fail"
	r.Errors = append(r.Errors, fmt.Sprintf("%s: %s", component, err.Error()))
}

// Register attaches the /healthz route. Routes are bound inside the
// OnServe handler in main.go; this function takes the serve event.
func Register(se *core.ServeEvent, version string) {
	se.Router.GET("/healthz", func(e *core.RequestEvent) error {
		r := Run(se.App, version)
		status := http.StatusOK
		if r.Status != "ok" {
			status = http.StatusServiceUnavailable
		}
		return e.JSON(status, r)
	})
}

// ErrUnhealthy is returned by Check (used by the CLI command) when any
// component fails. The CLI exits with code 1 in that case.
var ErrUnhealthy = errors.New("unhealthy")

// Check is the non-HTTP variant used by `qognical healthcheck`.
func Check(app core.App, version string) (*Result, error) {
	r := Run(app, version)
	if r.Status != "ok" {
		return r, ErrUnhealthy
	}
	return r, nil
}

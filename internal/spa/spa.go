// Package spa serves the embedded vanilla booking UI. Two routes:
//   - /book/{host}/{slug}       slot picker + intake + confirmation
//   - /manage/{booking_id}      view + reschedule + cancel
//
// The HTML files are go:embed-ed, so deployment stays single-binary.
// Framework choice (Svelte/Preact) is deferred to Phase 4 when the embed
// loader gets built; vanilla HTML+JS is enough to validate the API surface
// and the booking pipeline end-to-end.
package spa

import (
	"bytes"
	"embed"
	"net/http"
	"os"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

//go:embed web/index.html web/book.html web/manage.html web/embed.js web/form-builder.html web/host-login.html web/host-dashboard.html
var assets embed.FS

// Register attaches the SPA + embed-loader routes. orgName replaces the
// {{ORG_NAME}} placeholder in the served HTML so deployments show their own
// branding instead of the upstream project name.
func Register(se *core.ServeEvent, baseURL, orgName string) {
	if orgName == "" {
		orgName = "Terminbuchung"
	}
	// Origins allowed to iframe the public booking page. Configure via the
	// QOGNICAL_EMBED_ORIGINS env var (comma-separated, e.g. "https://acme.com,
	// https://www.acme.com"). Empty list = booking page refuses to be framed.
	// The end-state is a per-event-type allowlist; this global list is the
	// v1 single-tenant shortcut.
	var embedOrigins []string
	if raw := strings.TrimSpace(os.Getenv("QOGNICAL_EMBED_ORIGINS")); raw != "" {
		for _, o := range strings.Split(raw, ",") {
			if o = strings.TrimSpace(o); o != "" {
				embedOrigins = append(embedOrigins, o)
			}
		}
	}

	se.Router.GET("/", serveFile("web/index.html", orgName, nil))
	se.Router.GET("/book/{host}/{slug}", serveFile("web/book.html", orgName, embedOrigins))
	se.Router.GET("/manage/{id}", serveFile("web/manage.html", orgName, nil))
	se.Router.GET("/admin/forms", serveFile("web/form-builder.html", orgName, nil))

	// Host self-service console (SPA, hash-routing). Subpaths all resolve to
	// the same shell so deep-links and OAuth-callbacks land cleanly.
	se.Router.GET("/host/login", serveFile("web/host-login.html", orgName, nil))
	se.Router.GET("/host/dashboard", serveFile("web/host-dashboard.html", orgName, nil))
	se.Router.GET("/host/event-types", serveFile("web/host-dashboard.html", orgName, nil))
	se.Router.GET("/host/availability", serveFile("web/host-dashboard.html", orgName, nil))
	se.Router.GET("/host/integrations", serveFile("web/host-dashboard.html", orgName, nil))

	se.Router.GET("/embed.js", serveEmbed())
}

func serveEmbed() func(e *core.RequestEvent) error {
	body, err := assets.ReadFile("web/embed.js")
	if err != nil {
		panic(err)
	}
	return func(e *core.RequestEvent) error {
		h := e.Response.Header()
		h.Set("Content-Type", "application/javascript; charset=utf-8")
		// Long cache + version pinning by URL would be added in Phase 5
		// with content-hash filenames. For v1.0, 5min caching.
		h.Set("Cache-Control", "public, max-age=300")
		h.Set("Access-Control-Allow-Origin", "*") // loader is intentionally public
		return e.Blob(http.StatusOK, "application/javascript; charset=utf-8", body)
	}
}

func serveFile(name, orgName string, embedOrigins []string) func(e *core.RequestEvent) error {
	body, err := assets.ReadFile(name)
	if err != nil {
		// Fail loud at startup, not on first request.
		panic(err)
	}
	body = bytes.ReplaceAll(body, []byte("{{ORG_NAME}}"), []byte(orgName))

	// Pre-compute the CSP frame-ancestors directive once. When the page
	// allows embedding we extend the base policy and drop X-Frame-Options
	// for that route only — keeping admin/dashboard pages strict.
	csp := "default-src 'self'; style-src 'unsafe-inline' 'self'; " +
		"script-src 'unsafe-inline' 'self'; img-src 'self' data:"
	allowEmbed := len(embedOrigins) > 0
	if allowEmbed {
		ancestors := "'self'"
		for _, o := range embedOrigins {
			ancestors += " " + o
		}
		csp += "; frame-ancestors " + ancestors
	} else {
		csp += "; frame-ancestors 'self'"
	}

	return func(e *core.RequestEvent) error {
		h := e.Response.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", csp)
		if allowEmbed {
			// PocketBase's default activity-logger middleware emits
			// X-Frame-Options: SAMEORIGIN. Clear it for embed-enabled
			// routes so iframes on QOGNICAL_EMBED_ORIGINS actually render.
			// The CSP frame-ancestors above is the authoritative allow-list
			// — X-Frame-Options is the legacy fallback.
			h.Del("X-Frame-Options")
		}
		return e.Blob(http.StatusOK, "text/html; charset=utf-8", body)
	}
}

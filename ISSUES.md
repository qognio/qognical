# ISSUES — autonom getroffene Entscheidungen für Review

Lebendes Dokument. Jeder Eintrag ist eine Entscheidung, die ich während der
autonomen Implementierung getroffen habe und beim Review explizit besprechen
will. Format: Kontext → Optionen → Entscheidung → Folge-Aufwand falls Umkehr.

## Offen aus Phase 1 (vor Phase 2 zu klären)

### P1-I1 — `users` collection erweitert statt eigene `hosts` anlegen
Kontext: PocketBase liefert default `users`-Auth-Collection (system migration
1640988000). Mein Versuch eine zweite "users" anzulegen schlug fehl.
Optionen: (a) augment vorhandene, (b) eigene "hosts" anlegen.
Entscheidung: (a). Behält Naming-Konsistenz mit Doc 04.
Folge-Aufwand bei Umkehr: ein Migrations-Patch, alle Relations umbiegen.

### P1-I2 — Audit-log API-locked via nil rules statt expliziter Filter
Kontext: PocketBase-Filter-DSL akzeptiert "...&& false" nicht. INV-10 verlangt
Append-Only.
Entscheidung: ListRule/UpdateRule/DeleteRule = nil → API verboten außer für
Superuser. Inserts via Superuser-Connection im App-Code. Cron-Cleanup ebenfalls.
Konsequenz: kein "host darf eigenen audit_log lesen" via API. Wenn Bedarf,
Phase 4 mit View-Collection für gefilterten Auszug.

## Phase 2

### P2-I1 — availability.weekday darf nicht Required sein
Kontext: PocketBase NumberField mit `Required=true` lehnt den Wert 0 als
"missing" ab (`Required will require the field value to be non-zero`).
Unsere Konvention "0=Mo..6=So" macht Montag dadurch unspeicherbar.
Entscheidung: Required entfernt, Min/Max bleibt. App-Layer prüft Bereich.
Folge: alternative Migration (1..7 oder SelectField mit Wochentag-Strings)
beim Phase-2-Review klären.

### P2-I2 — `dbx.NewExp("IN ({:slice})")` funktioniert nicht
Kontext: PocketBase's dbx-Layer expandiert `[]string`/`[]any` nicht in
IN-Klauseln (`unsupported type []interface {}`).
Entscheidung: `ActiveBusyForHost` schreibt die aktiven Statuswerte inline
("draft","pending_payment","processing","confirmed"). Bei Änderung an
`state.ActiveStatuses()` muss die SQL nachgezogen werden.

### P2-I3 — Host-URL nutzt E-Mail-Alias statt eigenem Slug
Kontext: `users` hat keinen `slug`-Field. Public-URL ist heute
`/event-types/<email>/<event-slug>`. Funktioniert, ist aber unschön
(E-Mail-Enumeration trivial; URL-Encoding nötig).
Entscheidung: in Phase 4 `users.slug` einführen mit Uniqueness +
Reservierung gegen reserved words.

### P2-I4 — Slot-Validierung nutzt `slot.ComputeSlots` als Recheck
Kontext: Pipeline.validate ruft ComputeSlots auf einem Mini-Fenster ±1min.
Eleganter als eigene Window-Berechnung zu duplizieren, kostet aber pro
Buchung 2 DB-Queries (availability + overrides).
Entscheidung: für Phase 2 ok, in Phase 5 ggf. extrahieren als reine
"isValidStart"-Funktion ohne Slot-Enumeration.

### P2-I5 — Booking-SPA ist Vanilla HTML/JS
Kontext: Doc 00 empfiehlt Svelte oder Preact. Phase 4 hat den
"Framework-Wahl"-Punkt.
Entscheidung: für Phase 2 vanilla HTML+JS in `internal/spa/web/`,
go:embed-eingebunden. Framework-Entscheidung in Phase 4 separater ADR.

### P2-I6 — Pay/Meeting/Calendar-Schritte sind no-op Stubs
Pipeline-Engine deckt heute validate→intake→price→reserve→notify ab.
Paid Event-Types (`payment_mode != none`) werden auf `pending_payment`
gesetzt, aber ohne CheckoutURL. Phase 3 füllt das.

### P2-I7 — Cron-Idempotenz beim Reminder-Versand
Kontext: Reminder-Dispatcher feuert alle 5min und matched Slots
in einem 5-min-Fenster. Bei Cron-Drift / Restart in der Schaltminute
kann ein Reminder doppelt versendet werden.
Entscheidung: für Phase 2 akzeptiert. Phase 3 schreibt eine
`notifications_log`-Eintrag pro (booking, kind) und prüft Existenz vor
dem Senden.

### P2-I8 — Notifier-Fehler werden geloggt aber nicht retried
Phase-2-Notifier ist fire-and-forget. SMTP-Aussetzer führen zu fehlenden
Bestätigungen ohne automatischen Retry.
Entscheidung: Phase 3 implementiert `notifications_log`-Retry-Loop
mit Exponential Backoff (analog Outbound-Webhooks).

## Phase 3

### P3-I1 — Adapter sind stdlib-only (keine Provider-SDKs)
Bewusste Entscheidung gemäß Userdirektive "möglichst wenig deep dependencies".
Jeder Adapter (msgraph, google, nextcloud, jitsi, stripe, paypal) implementiert
HTTP-Calls direkt mit `net/http` + `encoding/json`. Tradeoff: ~150-300 LOC pro
Adapter (statt 20 mit SDK), aber Binary ≤ 32MB, keine transitive CVE-Oberfläche.

### P3-I2 — Payment-Provider sind Env-konfiguriert, nicht in `integrations`
Calendar-Provider werden pro Host in der `integrations`-Collection persistiert
(verschlüsselt). Stripe + PayPal kommen aus QOGNICAL_STRIPE_*/QOGNICAL_PAYPAL_*
Env-Vars — eine Konfiguration pro qognical-Instanz. Phase 5 könnte
Multi-Account-Stripe via Connect addieren; in v1.0 reicht single-account-pattern.

### P3-I3 — Outbound-Webhook-Cron läuft jede Minute (Test-Latenz)
Smoke-Test braucht ~70s bis ein Webhook beim Empfänger ankommt. Phase 5 sollte
einen `qognical webhooks deliver-now` CLI ergänzen für Ops-Diagnose.

### P3-I4 — Inbound-Webhook-Verarbeitung läuft in Goroutine ohne Persistenz
Stripe/PayPal Webhook-Handler antwortet 200 sofort und dispatched async per
`go func()`. Bei Server-Restart in genau dem Moment geht der Event verloren —
aber das Provider-Retry liefert ihn erneut (Idempotenz greift). Doku-Hinweis
in INTEGRATIONS/stripe.md ergänzt.

### P3-I5 — PayPal-Webhook-Verifikation per Callback statt offline
PayPal verifiziert Webhook-Signaturen via `verify-webhook-signature` API
(synchroner Roundtrip zu PayPal). Offline-Verifikation wäre möglich, aber
die PEM-Zertifikate rotieren — der "supported" Weg ist der API-Call. Kostet
~200ms pro Webhook, akzeptabel.

### P3-I6 — `meeting_config` ist types.JSONRaw aber Adapter erwarten json.RawMessage
Pipeline castet via `[]byte(...)` an die Adapter-Factory. Funktioniert, aber
das Type-Mismatch bei PocketBase JSONRaw und stdlib json.RawMessage ist
unschön. Refactor in Phase 5 (entweder PB-Cast in `store.go` oder eigenes
`adapters.JSONConfig`-Alias).

### P3-I7 — Hold-Mode für PayPal nicht in v1.0
Stripe unterstützt Hold via `capture_method=manual` direkt. PayPal hätte das
auch (Auth/Capture-Split), aber die UX und API sind aufwendiger. v1.0:
PayPal nur für `fixed`/`deposit`/`open`. Doku in INTEGRATIONS/paypal.md.

## Phase 4 + 5

### P4-I1 — Captcha + Rate-Limit standardmäßig aus
`QOGNICAL_CAPTCHA_PROVIDER` ist optional; ohne Wert läuft die Booking-API
ohne Captcha (Noop-Verifier). Rate-Limit-Defaults sind generös (60/min Reads,
5/min Mutations). Für öffentliche Production-Instanzen explizit konfigurieren.

### P4-I2 — Service-Token nutzt PocketBase Superuser-Auth fürs Anlegen
`/api/admin/v1/service-tokens` verlangt `e.HasSuperuserAuth()`. Pro Host wäre
noch eine Role-Trennung schöner ("host darf eigene Tokens anlegen") — aber für
v1.0 reicht Admin-Only.

### P4-I3 — embed.js postMessage-Origin-Check nutzt Loader-Script-Origin
Die Origin-Verification basiert darauf, dass das Loader-Script aus der
qognical-Instanz kommt. Ein Angreifer der `embed.js` lokal kopiert + manipuliert
hat damit immer noch Zugriff — aber er hat dann auch das eigene Embedding
unter Kontrolle, also kein Eskalations-Pfad.

### P4-I4 — React-Wrapper nicht gebaut, nicht publiziert
`packages/embed-react/` enthält Source + package.json + tsconfig. `tsup`-Build
und `npm publish` macht ein Operator/CI später — wir committen nur das
publish-bereite Source-Layout.

### P4-I5 — Integrations-Plaintext muss Operator manuell wegräumen
`qognical integrations set --credentials-file=...` liest den Klartext aus einer
Datei. Operator ist verantwortlich für das Löschen der Quelldatei nach dem
Setup. Phase 5.x: stdin-Variante (`--credentials-stdin`) für noch sauberere
Pipes.

### P5-I1 — SEC-Test-Suite läuft nicht in CI
`scripts/sec-tests.py` ist geschrieben + manuell ausgeführt; eine automatisch-
laufende Variante in `.github/workflows/` müsste a) einen Server in CI booten
b) Fixtures seeden c) Tests gegen ihn fahren. Sinnvoll als nächste Iteration.

### P5-I2 — Service-Token kein eigener Rate-Limit-Bucket
Die Mutex-Logic in `internal/api/api.go::rateLimitMiddleware` bypassed Auth-
Bearer komplett — kein Service-Bucket im Sinne von Doc 06 ("600 Req/Min pro
Token"). Heute teilen sich alle Service-Tokens den Rate-Limit-Pool der nicht
existiert. v1.1 Material.

### P5-I3 — Performance load-test gegen single SQLite-Datei
SQLite-WAL hat einen Single-Writer; alle Buchungen serialisieren. Bei sehr
hoher Concurrency (>50 parallel POSTs) sieht man das in der p95-Latenz. Per
Doc 03 ist das Skalierungs-Ziel "bis ~1000 Buchungen/Tag pro Instanz", was
sich gut unterhalb der Schmerzgrenze bewegt.

### P3-I11 — UC-4 hold capture_method nicht aus REST sichtbar (Smoke-Limit)
Stripe gibt `payment_intent_data[capture_method]` als write-only Param entgegen
und echo'ed es nicht beim Session-Retrieve. Erst wenn der Invitee den Checkout
öffnet, entsteht der PaymentIntent mit `capture_method=manual` sichtbar.
**Smoke-Strategie**: form-body-Korrektheit per Unit-Test
(`internal/adapters/stripe/stripe_test.go::TestCheckoutHoldUsesManualCapture`),
real-account-Test verifiziert nur Session-Shape (mode=payment, amount korrekt,
status=open). Volle Browser-UAT bleibt Phase 5 / manuell.

### P3-I10 — `charge.refunded` ohne client_reference_id-Lookup
Stripe's `charge.refunded` Event referenziert nur die charge/payment_intent,
nicht die client_reference_id (die nur auf checkout.session.* steht). Unsere
VerifyWebhook setzt BookingID daher leer; DispatchPayment lehnt das ab
("webhook missing booking id").
Fix für Phase 5: bei refund-events `payment_external_id`-Lookup in der
bookings-Tabelle, dann Status auf refunded transitionieren. Workaround
heute: Refund manuell per Host-UI markieren.

### P3-I9 — `pending_payment → confirmed` direkt erlaubt (entdeckt im Stripe-Smoke)
Doc 04 sieht `pending_payment → processing → confirmed` als Standard-Pfad.
Stripe's `checkout.session.completed` ist aber direkt der Success-Event, ohne
zwischenzeitliche "User-auf-Stripe-Seite"-Markierung. Test brachte
`illegal transition "pending_payment" → "confirmed"` zu Tage.
Fix: zusätzliche Kante `pending_payment → confirmed` in state.allowed.
Processing bleibt valid für SEPA-async-Flows wo Stripe selbst zwischen
Async-Pending und finalem Success unterscheidet.

### P3-I8 — `qognical superuser` kommando geht durch PocketBase, Encryption-Key nötig
Superuser-CLI ist von PocketBase übernommen — braucht jedoch trotzdem die
QOGNICAL_*-Pflicht-Env-Vars (sonst meckert needsStrictConfig nicht, aber das
Booking-Layer-Setup würde scheitern). In der Praxis: alle Pflicht-Vars
setzen oder eigene Setup-CLI bauen.

### P2-I9 — Reschedule via Token nutzt View-Action
Token-Service hat `Cancel`/`Reschedule`/`View` als Actions. Spec hat sie
getrennt; aktuell ignorieren wir die Action-Validierung in `verifyTokenForBooking`
und akzeptieren jeden gültigen Token. Beleg: ein Mail-Link mit
View-Token kann auch reschedule/cancel ausführen.
Entscheidung: pragmatisch — Doc 06 sagt "one signed link in the email
lets the invitee inspect + mutate". Phase 4 prüfen, ob granulare
Action-Tokens gewünscht sind.

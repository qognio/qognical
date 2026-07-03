# CONFIGURATION

Vollständige Referenz aller `QOGNICAL_*` Env-Variablen.

## Konventionen

- Jede Variable mit Suffix `_FILE` liest aus einem Pfad (Docker-Secrets-Style).
  Beispiel: `QOGNICAL_ENCRYPTION_KEY_FILE=/run/secrets/key`. Die `_FILE`-Variante
  hat Vorrang über den direkt gesetzten Wert.
- Booleans: `true|false`, `1|0`, `yes|no`.
- Listen: kommasepariert, Whitespace egal.

## Pflicht-Variablen

Fehlt eine davon, startet die Anwendung mit Exit-Code 2 und einer Liste
aller fehlenden Werte.

| Variable | Beispiel | Beschreibung |
|---|---|---|
| `QOGNICAL_BASE_URL` | `https://book.example.com` | Öffentliche URL der Instanz, in Mails + Tokens eingebettet |
| `QOGNICAL_ENCRYPTION_KEY` | `...` (32-byte base64) | Master-Key, verschlüsselt `integrations.credentials`. Aus `qognical genkey` |
| `QOGNICAL_SMTP_HOST` | `smtp.example.com` | SMTP-Server für Bestätigungs-Mails |
| `QOGNICAL_SMTP_PORT` | `587` | SMTP-Port (Default 587, 465 für SMTPS) |
| `QOGNICAL_SMTP_USER` | `no-reply` | SMTP-Auth-User |
| `QOGNICAL_SMTP_PASSWORD` | `...` | SMTP-Auth-Passwort |
| `QOGNICAL_SMTP_FROM` | `no-reply@example.com` | Absender-Adresse für alle ausgehenden Mails |

## Häufige Optionen

| Variable | Default | Beschreibung |
|---|---|---|
| `QOGNICAL_DATA_DIR` | `/pb_data` | SQLite-DB + Uploads + Backups |
| `QOGNICAL_LISTEN_ADDR` | `0.0.0.0:8090` | TCP-Listener (in Docker via `--http`) |
| `QOGNICAL_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `QOGNICAL_LOG_FORMAT` | `json` | `json` (für Aggregatoren) oder `text` |
| `QOGNICAL_EMBED_ORIGINS` | _(leer)_ | Komma-Liste voller Origins (z. B. `https://acme.com`), die die öffentliche Buchungsseite `/book/{host}/{slug}` per iframe **einbetten** dürfen. Setzt dort CSP `frame-ancestors` + entfernt `X-Frame-Options`. Leer = Einbetten verboten. |
| `QOGNICAL_CORS_ALLOWED_ORIGINS` | _(leer)_ | Komma-Liste voller Origins für **CORS** (Cross-Origin-`fetch`/XHR auf die öffentliche API). Getrennt vom Embed oben. |
| `QOGNICAL_RATE_LIMIT_PUBLIC` | `60/min` | Per-IP Rate-Limit für Lese-Endpoints (`N/sec`, `N/min`, `N/hour`) |
| `QOGNICAL_RATE_LIMIT_BOOK` | `5/min` | Per-IP Rate-Limit für Mutationen (Buchung/Cancel/Reschedule) |
| `QOGNICAL_CAPTCHA_PROVIDER` | _(leer)_ | `hcaptcha` / `turnstile` / leer (kein Captcha) |
| `QOGNICAL_CAPTCHA_SITE_KEY` | | Captcha-Site-Key (vom Provider) |
| `QOGNICAL_CAPTCHA_SECRET` | | Captcha-Secret-Key |
| `QOGNICAL_DATA_RETENTION_BOOKINGS_DAYS` | _(leer = unbegrenzt)_ | Cron-Cleanup unbezahlter Buchungen nach N Tagen |
| `QOGNICAL_DATA_RETENTION_AUDIT_DAYS` | `90` | Audit-Log-Rotation |
| `QOGNICAL_DATA_RETENTION_NOTIFICATIONS_DAYS` | `365` | Notification-Log-Rotation |

## Calendar-Provider

Jede Variable ist optional — leerer Wert = Provider deaktiviert. Pro Host
wird die genutzte Integration zusätzlich in der `integrations`-Collection
mit verschlüsselten Credentials angelegt.

### Microsoft Graph (Calendar + Teams)

```
QOGNICAL_MSGRAPH_TENANT_ID=...
QOGNICAL_MSGRAPH_CLIENT_ID=...
QOGNICAL_MSGRAPH_CLIENT_SECRET=...
```

Pro Host Application Access Policy auf dessen Mailbox setzen — siehe
[`docs/integrations/msgraph.md`](docs/integrations/msgraph.md).

### Google Calendar (ADR-0001)

```
QOGNICAL_GOOGLE_CLIENT_ID=...apps.googleusercontent.com
QOGNICAL_GOOGLE_CLIENT_SECRET=...
QOGNICAL_GOOGLE_REDIRECT_URI=https://book.example.com/oauth/google/callback
```

OAuth Consent Screen mit Scope `.../auth/calendar` — siehe
[`docs/integrations/google.md`](docs/integrations/google.md).

### Nextcloud CalDAV

Instanz-weite Konfig entfällt — Credentials liegen ausschließlich pro Host
in `integrations.credentials`. Siehe
[`docs/integrations/nextcloud.md`](docs/integrations/nextcloud.md).

## Meeting-Provider

### Jitsi (JWT-Modus)

```
QOGNICAL_JITSI_BASE_URL=https://meet.example.org
QOGNICAL_JITSI_JWT_SECRET=...
QOGNICAL_JITSI_JWT_APP_ID=qognical
```

Public-Jitsi ohne JWT: in `event_types.meeting_config` nur `base_url` setzen.

### Teams

Wird durch MS-Graph-Integration mitgeliefert (Doc-05 "Weg 1") — Event-Type
`location_type=online_teams` setzen, sonst keine zusätzliche Config nötig.

## Payment-Provider

### Stripe

```
QOGNICAL_STRIPE_SECRET_KEY=sk_live_...
QOGNICAL_STRIPE_WEBHOOK_SECRET=whsec_...
QOGNICAL_STRIPE_API_VERSION=2026-01-28
```

Webhook im Stripe-Dashboard auf `https://book.example.com/webhooks/stripe` mit
Events: `checkout.session.completed`, `checkout.session.async_payment_succeeded`,
`checkout.session.expired`, `invoice.paid`, `charge.refunded`.

### PayPal

```
QOGNICAL_PAYPAL_CLIENT_ID=...
QOGNICAL_PAYPAL_CLIENT_SECRET=...
QOGNICAL_PAYPAL_MODE=live          # oder sandbox
QOGNICAL_PAYPAL_WEBHOOK_ID=WH-...
```

Webhook auf `https://book.example.com/webhooks/paypal`. Events:
`CHECKOUT.ORDER.APPROVED`, `PAYMENT.CAPTURE.COMPLETED`, `PAYMENT.CAPTURE.REFUNDED`.

## Secret-Bereitstellung über `_FILE`

Für jeden Secret-Wert existiert eine `_FILE`-Variante, die den Pfad zu einer
Datei mit dem Wert erwartet. Beispiel mit Docker Secrets:

```yaml
secrets:
  encryption_key:
    file: ./secrets/encryption_key
  stripe_key:
    file: ./secrets/stripe_key

services:
  qognical:
    secrets: [encryption_key, stripe_key]
    environment:
      QOGNICAL_ENCRYPTION_KEY_FILE: /run/secrets/encryption_key
      QOGNICAL_STRIPE_SECRET_KEY_FILE: /run/secrets/stripe_key
```

`_FILE` hat Vorrang über den direkten Wert.

## Konfigurations-Hierarchie

1. `QOGNICAL_<NAME>_FILE` (Inhalt der Datei, trailing-newline stripped)
2. `QOGNICAL_<NAME>` (direkter Env-Wert)
3. Default-Wert im Code (für optionale Variablen)

## Validierung

`qognical serve` läuft folgende Checks bei jedem Start:

1. Alle Pflicht-Variablen gesetzt? → sonst Exit 2 mit kompletter Fehlerliste.
2. `QOGNICAL_BASE_URL` hat `http://`/`https://`-Prefix? → sonst Exit 2.
3. `QOGNICAL_ENCRYPTION_KEY` ist gültiges 32-Byte-Base64? → sonst Exit 2.
4. Migrations idempotent angewandt → sonst Container-Crashloop mit Fehlerlog.

`qognical healthcheck --dir=/pb_data` läuft danach kontinuierlich (Docker
HEALTHCHECK) und prüft: DB lesbar, Schema vollständig (alle Collections
vorhanden).

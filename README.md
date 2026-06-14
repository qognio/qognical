# qognical

Selbst-hostbares, schlankes Open-Source-Booking-Tool. Ein Go-Binary mit
eingebettetem [PocketBase](https://pocketbase.io/), ein Container, ein Volume —
Alternative zu Calendly / cal.com für kleine Teams und für die Einbettung in
fremde Webseiten.

Produkt unter der Brand **Qognio**. Codename während der Planungsphase: SlimCal.

## Was qognical ist

- **Lightweight**: ein Binary (~32 MB Image), SQLite, kein externer Service nötig.
- **Selfhosted-first**: läuft auf einem 5-€-VPS in unter 10 Minuten.
- **Secure by default**: Pflicht-Encryption-Key, harte Start-Fehler bei
  Misskonfiguration, Audit-Log, signierte Tokens, CORS-Allowlist.
- **Modular**: alle Provider (MS Graph, Google Calendar, Nextcloud CalDAV,
  Jitsi, Teams, Stripe, PayPal, Outbound-Webhooks) optional & per Config
  zuschaltbar.
- **Open Source**: Apache 2.0.

## Quick Start

```bash
# 1. Generate an encryption key
docker run --rm ghcr.io/qognio/qognical:latest genkey

# 2. Create .env from the example and fill in BASE_URL + SMTP + the generated key
cp .env.example .env
$EDITOR .env

# 3. Boot
docker compose up -d

# 4. Open the admin UI and create your first superuser
open http://localhost:8090/_/
```

Vollständige Anleitung in [`INSTALL.md`](INSTALL.md), Detail-Konfig in
[`CONFIGURATION.md`](CONFIGURATION.md).

## Features (v1.0)

### Booking-Kern
- Event-Types mit Verfügbarkeit (Wochenrhythmus + Date-Overrides)
- Slot-Berechnung mit Buffer, Min-Notice, Max-Horizon
- Buchungs-Pipeline (validate → intake → price → reserve → pay → meeting → calendar → notify)
- Cancel/Reschedule via signiertem Token (Single-Use, INV-8)
- iCal-Anhang in Bestätigungs-Mails
- DSGVO-Export + -Löschung als CLI

### Integrationen
| Familie | Provider |
|---|---|
| Calendar | MS Graph, Google Calendar, Nextcloud CalDAV |
| Meeting | Jitsi (offen + JWT), Microsoft Teams (Weg 1) |
| Payment | Stripe (Checkout/fixed/deposit/hold/subscription), PayPal Orders v2 |
| Notify | SMTP (iCal), Outbound-Webhooks (HMAC-signed, retry mit Backoff) |

### Embed
- `embed.js`-Loader (~3 KB gzipped) mit Inline-, Popup-, Floating-Button-Modi
- React-Wrapper-Paket [`@qognical/embed-react`](packages/embed-react/)
- Headless Public-API für eigene UIs
- Agentic: **Service-Tokens** (per ADR-0002) für Bots/Voicebots/CRMs

### Operative Sicht
- Healthcheck-Endpoint + CLI-Subkommando
- Audit-Log (append-only), Notification-Log, Webhook-Delivery-Log
- Automatische Schema-Migrationen beim Container-Start
- Cron-Jobs für Reminder, Cleanup, Outbound-Delivery

## Architektur

```
┌──────────────────────────────────────────────────────┐
│ Container (Alpine, <50 MB)                           │
│                                                      │
│  ┌────────────────────────────────────────────────┐  │
│  │ qognical (Go binary)                           │  │
│  │                                                │  │
│  │  PocketBase Core (SQLite, Auth, Admin, Cron)   │  │
│  │  Booking-Layer (Pipeline, Slots, Tokens, API)  │  │
│  │  Provider-Adapter (Cal/Meeting/Payment)        │  │
│  │  Notifier (SMTP, Outbound-Webhooks)            │  │
│  │  Embed-SPA + embed.js (go:embed)               │  │
│  └────────────────────────────────────────────────┘  │
│                                                      │
│   Volume: /pb_data (SQLite + Uploads + Backups)      │
└──────────────────────────────────────────────────────┘
```

Detail: [`docs/planning/03-architecture.md`](docs/planning/03-architecture.md).

## Dokumentation

- [`INSTALL.md`](INSTALL.md) — Setup auf VPS / Docker-Compose
- [`CONFIGURATION.md`](CONFIGURATION.md) — vollständige Env-Variablen-Referenz
- [`SECURITY.md`](SECURITY.md) — Threat-Model, Disclosure-Prozess, Verantwortungsgrenzen
- [`PRIVACY.md`](PRIVACY.md) — DSGVO, Aufbewahrungsfristen, Datenfluss
- [`UPGRADING.md`](UPGRADING.md) — Versions-Update-Pfad
- [`docs/integrations/`](docs/integrations/) — pro Provider eine Setup-Anleitung
- [`ROADMAP.md`](ROADMAP.md) — was als nächstes kommt
- [`KNOWN_LIMITATIONS.md`](KNOWN_LIMITATIONS.md) — was qognical bewusst (noch)
  nicht tut

## Status

Aktuelle Version: **v1.0.0** (initial public release).

Eine Liste der bewussten v1.0-Trade-offs findest du in
[`KNOWN_LIMITATIONS.md`](KNOWN_LIMITATIONS.md); was geplant ist in
[`ROADMAP.md`](ROADMAP.md).

## Lizenz

Apache License 2.0 — siehe [`LICENSE`](LICENSE).

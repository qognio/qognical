# SECURITY

## Reporting a vulnerability

Bitte **keine öffentlichen GitHub-Issues** für Sicherheitsprobleme öffnen.
Stattdessen direkt an: **`security@qognio.com`** (PGP-Key folgt mit v1.0
final). Wir bestätigen den Eingang innerhalb von **72 Stunden**.

Coordinated Disclosure: bestätigte Findings werden binnen 30 Tagen gefixt
und ein CVE wird beantragt, sofern relevant. Security-Advisories erscheinen
als GitHub-Releases mit dem `security`-Label.

## Threat-Model (Kurzform)

Detaillierte Threat-Tabellen in [`docs/planning/07-security-and-privacy.md`](docs/planning/07-security-and-privacy.md).

### Was qognical adressiert

| Bedrohung | Mechanismus |
|---|---|
| Diebstahl Provider-Credentials aus DB | AES-GCM Encryption at-rest, Schlüssel aus `QOGNICAL_ENCRYPTION_KEY` (Pflicht beim Start) |
| Doppelbuchung durch Race | Transaktionaler Lock vor Insert (INV-1) |
| Gefälschte Payment-Webhooks | HMAC-Signatur-Verify auf Roh-Body, ±5min Replay-Window |
| Spam-Buchungen | Per-IP Rate-Limit + optional Captcha (hCaptcha/Turnstile) |
| Free/Busy-Enumeration | Public-API gibt nur freie Slots, nie belegte mit Details (INV-7) |
| Clickjacking | CSP `frame-ancestors`-Allowlist über `QOGNICAL_CORS_ALLOWED_ORIGINS` |
| postMessage-Hijacking | Beidseitige Origin-Prüfung in embed.js |
| Token-Replay (Cancel/Reschedule) | Single-Use via Hash-Rotation (INV-8) |
| XSS in Intake-Form | Server-side Schema-Validierung + Context-aware escaping in Mail/iCal |
| SQL-Injection | PocketBase Prepared Statements (kein raw SQL aus User-Input) |
| Audit-Log-Manipulation | API write/delete = nil rule → nur Superuser via direkter DB |
| Service-Token-Leak | Bearer-Token nur sha256-gehasht persistiert, Scope + Host-Binding |

### Verantwortung des Betreibers

| Bedrohung | Wer mitigiert |
|---|---|
| TLS / Network-Sniffing | Reverse-Proxy (Caddy/Traefik/nginx) — qognical spricht nur HTTP intern |
| Volume-/Disk-Diebstahl | Host-FS-Encryption (LUKS, EBS encryption, BitLocker) |
| Backup-Diebstahl | Verschlüsselte off-site Backups (restic auf S3/B2 empfohlen) |
| Kompromittierter Host | Patch-Management, regelmäßige Image-Updates |
| Insider-Threats | OS-Level Access-Control |
| DDoS Volumen-Ebene | CDN/WAF vor der Instanz (Cloudflare, BunnyCDN) |

## Sichere Defaults

Beim Start verweigert qognical, wenn:

- `QOGNICAL_ENCRYPTION_KEY` fehlt oder kein gültiges 32-Byte-Base64 ist
- Pflicht-SMTP-Variablen fehlen
- `QOGNICAL_BASE_URL` kein `http(s)://`-Prefix hat

CORS-Allowlist ist standardmäßig **leer** — explizit-opt-in für Cross-Origin.
Captcha ist standardmäßig **aus** — explizit-opt-in für die Mutations-Endpoints.

## Verschlüsselung im Detail

- **Master-Key**: 32 Bytes aus `QOGNICAL_ENCRYPTION_KEY` (base64-decoded)
- **Sub-Keys**: HKDF-SHA256 derivation für drei Zwecke (sie sind paarweise verschieden):
  - Credential-Encryption (AES-GCM auf `integrations.credentials`)
  - URL-Token-Signing (HMAC-SHA256 für Cancel/Reschedule/View-Tokens)
  - Outbound-Webhook-Signing (HMAC-SHA256 für `X-Qognical-Signature`)
- **Key-Rotation**: `qognical rotate-key` (v1.1) — re-encrypts alle credentials
- **Service-Tokens**: nicht mit dem Master-Key verschlüsselt, sondern nur als
  sha256-Hash persistiert (analog Passwörter). Token werden beim Anlegen
  einmalig sichtbar gemacht.

## Audit-Logging

Sicherheitsrelevante Events werden in `audit_log` geschrieben:

- `booking.created` / `cancelled` / `rescheduled` mit Actor + IP
- `service_token.created` / `revoked` (Admin-Auth)
- `webhook.processed` (Provider + Event-ID, für Idempotenz)
- `dsgvo.delete` (Anonymisierung)
- `notify.failed` (SMTP-Fehler)
- `login.failed` (PocketBase, mit Brute-Force-Lockout)

`audit_log` ist via API für Nicht-Superuser nicht beschreibbar oder löschbar
(INV-10). Aufbewahrungsfrist konfigurierbar (`QOGNICAL_DATA_RETENTION_AUDIT_DAYS`,
Default 90).

## Was wir bewusst nicht tun

- **Kein Bug-Bounty in v1.0** — kostet Geld + Aufmerksamkeit, die ein
  junges OSS-Projekt nicht hat. Reports sind willkommen, danke sagen wir
  per Hand.
- **Kein eigener Pentest** vor v1.0 — stattdessen Code-Review-Sessions,
  Threat-Modeling, automatisierte SEC-Tests (`scripts/sec-tests.py`).
- **Keine Zertifizierungen** (SOC2, ISO27001) — qognical ist selfhosted;
  Zertifizierungen sind Betreiber-Sache, nicht Projekt-Sache.

## Safe-Integration-Pattern

Wenn qognical in deine Bestandssysteme integriert wird:

1. **Behandle die qognical-Instanz wie einen externen, untrusted Service.**
   Selbst wenn du sie selbst betreibst.
2. **Nur über offizielle Public-API + Outbound-Webhooks** — keine geteilte DB,
   kein Filesystem-Sharing.
3. **Pull statt Push** für sensible Aktionen.
4. **Netzwerk-Segmentierung**: qognical in eigenem Segment, Egress nur zu
   erlaubten Provider-APIs + euren Webhook-Sinks.
5. **Service-Tokens nicht teilen** — pro Konsument ein Token mit
   minimalen Scopes.

Detail: [`docs/planning/07-security-and-privacy.md`](docs/planning/07-security-and-privacy.md) Abschnitt "Integration in eigene Systeme".

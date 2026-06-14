# PRIVACY (DSGVO)

qognical ist als selfhosted-Tool ausgelegt — der Betreiber ist DSGVO-Verantwortlicher
nach Art. 4 Abs. 7. Dieses Dokument hilft, ein Verzeichnis von Verarbeitungstätigkeiten
(Art. 30 DSGVO) aufzustellen und beschreibt die Werkzeuge, die qognical für die
Erfüllung der Betroffenenrechte bereitstellt.

## Verarbeitete Datenkategorien

| Kategorie | Personenbezug | Speicherort | Aufbewahrung (Default) |
|---|---|---|---|
| Host-Account (E-Mail, Name, TZ, Rolle) | direkt | `users` | bis Account-Löschung |
| Host-Verfügbarkeit / Event-Type-Config | indirekt | `availability` / `event_types` | bis Löschung des Records |
| Invitee (Name, E-Mail, optional Telefon) | direkt | `bookings.invitee_*` | siehe unten |
| Intake-Form-Antworten | potenziell sensibel | `bookings.intake_data` | siehe unten |
| Notification-Log | indirekt (E-Mail-Adresse) | `notifications_log` | 365 Tage (konfigurierbar) |
| Audit-Log | direkt (User-ID, optional IP) | `audit_log` | 90 Tage (konfigurierbar) |
| Webhook-Delivery-Log | direkt im Payload | `webhook_deliveries` | 30 Tage |
| Stripe / PayPal IDs | indirekt | `bookings.payment_external_id` | wie Buchung |
| Provider-Credentials | indirekt | `integrations.credentials` (verschlüsselt) | bis Widerruf |

### Buchungs-Aufbewahrung

| Status | Default |
|---|---|
| `confirmed` (bezahlt) | unbegrenzt (Rechnungs-Aufbewahrung typ. 10 Jahre) |
| `confirmed` (kostenlos) | 12 Monate nach Termin |
| `cancelled` / `abandoned` | 12 Monate |
| `intake_data` | wie Buchung; optional früher per `delete-data`-CLI anonymisierbar |

Per `QOGNICAL_DATA_RETENTION_BOOKINGS_DAYS` global überstimmbar.

## Werkzeuge für Betroffenenrechte

### Auskunft (Art. 15) — Datenexport

```bash
docker compose exec qognical /qognical export-data --email=invitee@example.com
```

Liefert eine JSON-Datei mit allen Buchungen, Intake-Antworten, Notifikations-
Logs zu dieser Adresse. Direkt an den Betroffenen weitergebbar.

### Löschung (Art. 17) — Anonymisierung

```bash
docker compose exec qognical /qognical delete-data \
  --email=invitee@example.com --yes
```

Setzt `invitee_name` und `invitee_email` auf `anonymised`/`anonymised@invalid.local`,
leert `invitee_phone` + `intake_data` + `cancellation_reason`. Booking-ID und
Termin-Zeitraum bleiben für statistische Auswertung erhalten. Der Vorgang ist
**irreversibel** — `--yes` Flag bestätigt das.

Ein Audit-Log-Eintrag mit `action=dsgvo.delete` wird geschrieben (für die
Nachweisbarkeit gegenüber Aufsichtsbehörden).

### Berichtigung (Art. 16)

Über die PocketBase Admin-UI unter `Collections → bookings → <id> → Edit`.

### Widerspruch (Art. 21)

In der Praxis: Anonymisierung wie oben oder Cancel-Token in der ursprünglichen
Bestätigungs-Mail nutzen.

## Cookies & Tracking

- **Booking-SPA**: keine Tracking-Cookies, kein Analytics-Tracker.
- **Admin-UI**: nur Session-Cookies für Auth, kein Drittanbieter-Code.
- **Einbettende Seite**: ist selbst verantwortlich für ihr Tracking.

## Drittanbieter, an die Daten übermittelt werden

Pro aktiver Integration. Listet auf, was an wen geht:

| Empfänger | Voraussetzung | Übermittelte Daten | Zweck |
|---|---|---|---|
| Microsoft (Graph) | `msgraph`-Integration | Event-Titel + Start/Ende + Teilnehmer-E-Mail | Kalender-Schreiben |
| Google | `google`-Integration | wie oben | Kalender-Schreiben |
| Nextcloud-Hoster | `nextcloud`-Integration | wie oben (als ICS-Datei) | Kalender-Schreiben |
| Stripe (USA) | `stripe`-Aktivierung | Invitee-E-Mail, Betrag, Booking-ID | Zahlungsabwicklung |
| PayPal | `paypal`-Aktivierung | wie Stripe | Zahlungsabwicklung |
| Jitsi-Hoster | `jitsi`-Aktivierung | Booking-ID (in Raum-Name + JWT-Claims) | Meeting-Bereitstellung |
| Captcha-Provider | `hcaptcha`/`turnstile` aktiv | IP, User-Agent, Captcha-Lösung | Spam-Schutz |
| SMTP-Provider | immer (Pflicht-Config) | Invitee-E-Mail, Bestätigungstext | Mail-Zustellung |

Vor Aktivierung eines Drittanbieters: **DPA** (Auftragsverarbeitungsvertrag)
und ggf. **Übermittlungsmechanismus** (Art. 44ff.) prüfen. Für USA-Anbieter
(Stripe, MS, Google) zusätzlich SCCs oder DPF-Status sichern.

## Datensparsamkeit per Design

- **Public-API** gibt nie Host-E-Mail, fremde Buchungen, andere Slots-Details
  zurück (INV-7).
- **Outbound-Webhooks** transportieren nur den Booking-Payload — keine
  Credentials, keine fremden Buchungen, keine Host-E-Mail (außer explizit
  gewünscht in v1.x).
- **Audit-Log** speichert IP-Adressen *im Klartext* nur dort; Request-Logs
  hashen die IP (`X-Real-IP` SHA256-gehasht).
- **Reminder-Cron** sendet nur an den ursprünglichen Invitee, nie an
  Drittadressen.

## Standorte

qognical selbst läuft auf deinem Server. Die folgenden Standorte sind
**ausschließlich** dann relevant, wenn die jeweilige Integration aktiv ist:

| Provider | Region |
|---|---|
| Microsoft Graph | EU + USA (M365-Tenant-Region maßgeblich) |
| Google Calendar | weltweit, primär USA |
| Stripe | Irland (EU) für EU-Accounts |
| PayPal | Luxemburg (EU) |
| hCaptcha | USA |
| Cloudflare Turnstile | weltweit (Anycast) |

## Audit + Nachweisbarkeit

`audit_log` (siehe SECURITY.md) dokumentiert wer wann was getan hat.
`notifications_log` dokumentiert versendete Mails. `webhook_deliveries`
dokumentiert Outbound-Webhooks. Alle drei Logs sind über die Admin-UI
einsehbar und über die `QOGNICAL_DATA_RETENTION_*`-Variablen rotierbar.

Empfehlung: monatlicher Export von `audit_log` für die eigenen Akten,
plus Stichproben-Restore-Test der Backups (siehe INSTALL.md).

## Stand & Aktualisierung

Dieser Hinweis spiegelt die qognical v1.0 Default-Konfiguration. Bei
Aktivierung weiterer Provider oder bei Änderungen an der eigenen Konfig
muss dein eigenes Verarbeitungsverzeichnis aktualisiert werden.

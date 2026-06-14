# Provider Integrationen

Pro Provider eine eigene Datei mit Permission-Setup, Credentials-Shape und
typischen Fallstricken. Verbindliche Dokumentation für Betreiber.

| Provider     | Familie           | Datei                          |
|--------------|-------------------|--------------------------------|
| MS Graph     | Calendar + Teams  | [msgraph.md](msgraph.md)       |
| Google       | Calendar          | [google.md](google.md)         |
| Nextcloud    | Calendar (CalDAV) | [nextcloud.md](nextcloud.md)   |
| Jitsi        | Meeting           | [jitsi.md](jitsi.md)           |
| Stripe       | Payment           | [stripe.md](stripe.md)         |
| PayPal       | Payment           | [paypal.md](paypal.md)         |
| Outbound-Webhooks | Notify       | [webhooks.md](webhooks.md)     |

## Gemeinsame Designentscheidung

Jeder Adapter ist eine schlanke `net/http`-Implementierung — keine
Provider-SDKs. Das hält die Binary klein, reduziert CVE-Oberfläche und macht
jeden Adapter in unter 200 LOC lesbar.

Credentials werden verschlüsselt at-rest gespeichert (`integrations.credentials`,
AES-GCM mit Master-Key aus `QOGNICAL_ENCRYPTION_KEY`). Pro Host ist genau eine
Kalender-Integration aktiv (`sync_enabled=true`); Payment-Provider sind
instanz-scoped (eine Stripe-Konfig pro qognical-Instanz).

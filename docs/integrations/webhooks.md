# Outbound-Webhooks

qognical kann Buchungs-Events an externe Systeme pushen — ERPNext, n8n,
Chat-Channels mit Incoming-Webhook, eigene CRMs.

## Konfiguration

Eintrag in `outbound_webhooks` (über PocketBase-Admin oder DSGVO-CLI; Phase 4
bekommt ein Host-UI):

| Feld     | Wert                                                                  |
|----------|-----------------------------------------------------------------------|
| `owner`  | User-ID des Hosts (leer = global für alle Hosts der Instanz)          |
| `url`    | HTTPS-Ziel, z.B. `https://erpnext.example.com/api/qognical-receiver`  |
| `secret` | HMAC-Secret (frisch generieren, verschlüsselt at-rest)                |
| `events` | JSON-Array, z.B. `["booking.confirmed","booking.cancelled"]` oder `["*"]` |
| `active` | `true`                                                                |

## Events

| Event                  | Trigger                                              |
|------------------------|------------------------------------------------------|
| `booking.created`      | Buchung wurde angelegt (auch im Status pending_payment) |
| `booking.confirmed`    | Status wurde `confirmed` (direkt oder nach Payment)  |
| `booking.cancelled`    | Token-Cancel oder Host-Cancel                        |
| `booking.rescheduled`  | Verschoben                                           |
| `booking.refunded`     | Refund über Payment-Provider erfolgt                 |
| `payment.failed`       | Payment endgültig fehlgeschlagen                      |

## Payload-Schema

```json
{
  "event_id":    "evt_a1b2c3...",
  "event_type":  "booking.confirmed",
  "occurred_at": "2026-06-01T07:00:00Z",
  "data": {
    "booking_id":  "...",
    "event_type":  { "id": "...", "title": "...", "slug": "..." },
    "host":        { "id": "...", "name": "..." },
    "invitee":     { "name": "...", "email": "...", "phone": "..." },
    "start_utc":   "2026-06-01T09:00:00Z",
    "end_utc":     "2026-06-01T09:30:00Z",
    "meeting_url": "https://teams.microsoft.com/l/meetup-join/...",
    "intake_data": { "topic": "..." }
  }
}
```

Nicht im Payload: Host-E-Mail (außer explizit gewünscht in v1.x), Credentials,
fremde Buchungen, externe Provider-IDs.

## Signatur

Header pro Request:

```
X-Qognical-Signature: t=<unix_ts>,v1=<hex_sha256_hmac>
X-Qognical-Event-Id:  evt_a1b2c3...
X-Qognical-Timestamp: 1809123456
Content-Type: application/json
```

Verifizierung beim Empfänger:

```
signed_payload = "<ts>." + raw_body
expected       = HMAC-SHA256(secret, signed_payload)
match          = constant_time_eq(hex(expected), v1)
```

Empfänger sollte einen 5-Minuten-Replay-Window-Check ergänzen.

## Retry-Verhalten

- 2xx-Antwort = `delivered`, fertig.
- Non-2xx oder Timeout = Retry nach 1m, 5m, 30m, 2h, 12h. Danach `abandoned`.
- Idempotenz beim Empfänger: derselbe `X-Qognical-Event-Id` darf nur einmal
  wirken (qognical kann wegen Cron-Drift unter 5min-Auflösung mehrfach pushen).

## DSGVO

`payload` wird in `webhook_deliveries` 30 Tage aufbewahrt. Konfigurierbar
über `QOGNICAL_DATA_RETENTION_WEBHOOK_DELIVERIES_DAYS` (Phase 5).

# Stripe

Instanz-scoped: ein Stripe-Account pro qognical-Instanz. Konfiguration via
Env-Variablen:

```
QOGNICAL_STRIPE_SECRET_KEY=sk_live_...
QOGNICAL_STRIPE_WEBHOOK_SECRET=whsec_...
QOGNICAL_STRIPE_API_VERSION=2026-01-28
```

Webhook-Endpoint in Stripe-Dashboard auf
`https://book.example.com/webhooks/stripe` setzen mit Events:

- `checkout.session.completed`
- `checkout.session.async_payment_succeeded`
- `checkout.session.async_payment_failed`
- `checkout.session.expired`
- `invoice.paid`
- `charge.refunded`

## Payment-Modes Mapping

| `payment_mode`  | Stripe                                                           |
|-----------------|------------------------------------------------------------------|
| `fixed`         | Checkout Session, `mode=payment`, `line_items[0].price_data`     |
| `deposit`       | Wie `fixed` mit Teilbetrag                                       |
| `hold`          | `mode=payment` + `payment_intent_data[capture_method]=manual`    |
| `subscription`  | `mode=subscription` + `line_items[0].price={STRIPE_PRICE_ID}`    |
| `open`          | Wie `fixed`, vom Caller-Layer mit Mindestbetrag-Constraint       |

## Sicherheit

- **Signatur**: HMAC-SHA256 über `<timestamp>.<raw_body>`, Header
  `Stripe-Signature: t=...,v1=...`. qognical lehnt Replays älter als
  5 Minuten ab.
- **Idempotenz**: `event_id` wird im `audit_log` als `webhook.processed`
  Eintrag erfasst. Doppelte Zustellungen werden mit 200 OK quittiert, aber
  nicht verarbeitet.
- **SLA**: Webhook-Handler antwortet binnen <100 ms; die Pipeline-Arbeit
  (Calendar-Eintrag, Mail) läuft asynchron in einer Goroutine.
- **API-Version-Pinning**: `STRIPE_API_VERSION` setzen — Stripe-Default
  ändert sich nicht still.

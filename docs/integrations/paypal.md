# PayPal (Orders v2)

Instanz-scoped wie Stripe.

```
QOGNICAL_PAYPAL_CLIENT_ID=...
QOGNICAL_PAYPAL_CLIENT_SECRET=...
QOGNICAL_PAYPAL_MODE=live           # oder sandbox
QOGNICAL_PAYPAL_WEBHOOK_ID=WH-...
```

Webhook-Endpoint in PayPal-Dashboard auf
`https://book.example.com/webhooks/paypal` setzen mit Events:

- `CHECKOUT.ORDER.APPROVED`
- `PAYMENT.CAPTURE.COMPLETED`
- `PAYMENT.CAPTURE.DENIED`
- `PAYMENT.CAPTURE.REFUNDED`

## Payment-Modes

| `payment_mode` | Unterstützt | Notiz                                          |
|----------------|-------------|------------------------------------------------|
| `fixed`        | ✓           | One-shot Order, capture nach Approval          |
| `deposit`      | ✓           | Wie `fixed` mit Teilbetrag                     |
| `open`         | ✓           | Caller setzt amount                            |
| `hold`         | ✗ (v1.x)    | PayPal-Auth/Capture-Split möglich, scope-disziplin |
| `subscription` | ✓ (v0.3+)   | Plan-ID via `event_types.stripe_price_id` (siehe unten) |

## Sicherheit

## PayPal Subscriptions (v0.3)

Für Subscription-Event-Types:

1. In PayPal-Dashboard ein **Produkt** + **Billing-Plan** anlegen (z.B. monatlich).
2. Die **Plan-ID** (Format `P-...`) ins Event-Type-Feld `stripe_price_id`
   eintragen. Das Feld wird (historisch) für beide Provider verwendet.
3. Event-Type mit `payment_mode=subscription`, `payment_provider=paypal`.
4. Webhook-Events zusätzlich abonnieren: `BILLING.SUBSCRIPTION.ACTIVATED`,
   `BILLING.SUBSCRIPTION.CANCELLED`, `BILLING.SUBSCRIPTION.PAYMENT.FAILED`.

Bei Buchung wird `POST /v1/billing/subscriptions` aufgerufen, der Invitee
zur Approval-URL umgeleitet, anschließend macht das Webhook
`BILLING.SUBSCRIPTION.ACTIVATED` den Booking-Übergang auf confirmed.

## Webhook-Verifikation

PayPal-Webhook-Signaturen werden über `verify-webhook-signature` von PayPal
selbst verifiziert (offline-Verifikation ist möglich, aber die PEM-Zertifikate
rotieren). qognical schickt Headers + Body zurück und vertraut der Antwort.

Idempotenz analog Stripe via `audit_log`.

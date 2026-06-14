# Stripe Connect (Multi-Account)

v0.3.0 fügt einen "Connect"-Modus zum Stripe-Adapter hinzu, der jeden Host
auf seinen eigenen Stripe-Account routet. Geld fließt direkt auf das
Host-Konto; qognical kann optional eine `application_fee_amount` (Plattform-
Gebühr) abziehen.

## Einrichtung pro Host

1. **Connect Account anlegen** (qognical-CLI, demnächst):

   ```bash
   docker compose exec qognical /qognical stripe-connect create \
     --host=<users.id> --email=host@example.com --country=DE
   # → acct_1AbcDe…
   ```

   v0.3 noch ohne CLI — der Operator legt den Account im Stripe-Dashboard
   manuell an (Connect → Connected accounts) oder via Stripe-API:
   `POST /v1/accounts` mit `type=standard`.

2. **Onboarding-Link** generieren:

   ```bash
   curl -u $STRIPE_SECRET_KEY: https://api.stripe.com/v1/account_links \
     -d account=acct_1AbcDe… \
     -d type=account_onboarding \
     -d return_url=https://book.example.com/admin/stripe-onboard-done \
     -d refresh_url=https://book.example.com/admin/stripe-onboard-retry
   ```

   Den Host an die zurückgegebene URL schicken — Bankdaten, Identität,
   Steuer-Setup. Bis Onboarding fertig ist, kann der Account keine
   Auszahlungen empfangen, aber Test-Charges funktionieren bereits.

3. **integrations-Row anlegen** mit der Account-ID:

   ```bash
   echo '{"stripe_account_id":"acct_1AbcDe…"}' > /tmp/sc.json
   docker compose exec qognical /qognical integrations set \
     --owner=<users.id> --provider=stripe \
     --credentials-file=/tmp/sc.json --enable
   rm /tmp/sc.json
   ```

   *Hinweis: aktuell ignoriert die Pipeline diese Row noch und benutzt nur
   den Instance-Level-Key. Wiring kommt mit der CLI in v0.4.*

## Application Fee

Der Booking-Pipeline-Caller kann beim Anlegen des Checkouts ein
`ApplicationFeeCents`-Feld setzen (siehe `adapters.CheckoutRequest`). Beispiel:
5% Plattform-Gebühr auf 120 € = 600 cents.

## Webhook-Routing

Connect-Events tragen ein zusätzliches Feld `account` auf der Event-Hülle.
qognical's Inbound-Handler liest dieses Feld und gleicht es gegen die
integrations-Rows ab. v0.3 implementiert diese Verbindung noch nicht
vollständig — bis dahin müssen Connect-Webhooks auf einen separaten
Endpoint pro Account gehen, oder du nutzt Stripe Direct (Standard
ohne `account` field auf den Events).

## Mode-Übersicht

| Stripe-Modus | Stripe-Account | application_fee | qognical-Wiring |
|---|---|---|---|
| Standard (Instance-Level) | ein Account pro qognical-Instanz | nein | v0.1 |
| **Connect Standard** (v0.3) | pro Host ein Account | optional | siehe oben |
| Connect Express | pro Host, Stripe-managed | optional | nicht in v0.3 |
| Connect Custom | pro Host, qognical hostet alles | optional | nicht in v0.3 |

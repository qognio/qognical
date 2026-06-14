# scripts/

Operator helpers, not Phase-1 build artefacts.

## smoke-stripe.py

End-to-end smoke against a real Stripe test account. Loops through UC-2/3/4,
async SEPA, idempotency, expired, tamper, replay-window.

```bash
# Boot qognical with matching Stripe env (see .env.example).
QOGNICAL_STRIPE_SECRET_KEY=sk_test_... \
QOGNICAL_STRIPE_WEBHOOK_SECRET=whsec_... \
qognical serve

# Seed once
QOGNICAL_ADMIN_PASSWORD=AdminPass123 scripts/seed.sh

# Run the smoke (must use the SAME whsec_ and a Stripe key on the same account)
QOGNICAL_ADMIN_PASSWORD=AdminPass123 \
STRIPE_SECRET_KEY=sk_test_... \
STRIPE_WEBHOOK_SECRET=whsec_... \
python3 scripts/smoke-stripe.py
```

Last verified against the Katzenmalen Stripe Sandbox account on 2026-05-28
(8/8 tests green, real `cs_test_...` sessions visible in the Stripe Dashboard).

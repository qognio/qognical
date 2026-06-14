# Known limitations

Things qognical does not do — or does in ways operators should be aware of.
For planned improvements, see [ROADMAP.md](ROADMAP.md).

---

## Throughput ceiling

qognical uses an embedded SQLite database (via PocketBase). SQLite-WAL has a
**single writer**; all booking creates serialise. Practical throughput is
around **1,000 bookings/day per instance** before write-latency becomes
noticeable. If you need higher, run multiple instances behind a load balancer
(each with its own SQLite) or wait for the Postgres adapter in v2.0.

## Public host URLs leak email addresses

Booking URLs today are `/event-types/<host-email>/<event-slug>`. A trivial
script can enumerate hosts by trying common email addresses. This is a
deliberate Phase-2 simplification — v1.2 will introduce a `users.slug` field
with proper uniqueness, but **the security model never assumed host emails
are secret**. If yours are, run qognical on a non-public domain and gate
booking pages with a reverse-proxy IP allowlist.

## Single signed link per email is shared authority

When an invitee receives "View / Reschedule / Cancel" links by email, all
three are derived from a single token with the strongest authority the
operator granted. A Reschedule-link can also cancel; a Cancel-link can also
view. This is intentional — splitting them into three links pushes the
inbox UX past what most invitees tolerate. If you need stricter per-action
tokens, configure your event-type with `token_split=true` (planned for v1.2).

> Note: View-only tokens **do** stop at view — they cannot cancel or
> reschedule. The shared-authority rule only flows from stronger to weaker
> (Cancel → View, Reschedule → View), never the reverse.

## SMTP failures lose notifications

Notification delivery is fire-and-forget in v1.0. If your SMTP relay is
down for 30 seconds during a booking, the confirmation email is gone —
the booking itself still confirmed. The host can re-send manually from
the dashboard. v1.1 wraps notifications in the same retry queue that
outbound webhooks use.

## Reminder dispatcher can double-send on cron drift

The reminder cron matches bookings whose start time falls within a
5-minute window. If the cron process restarts inside that window, the
next tick re-matches the same bookings and re-sends. Acceptable for v1.0
(idempotency on the receiving end is usually fine for "your meeting starts
in 1 hour" reminders). v1.1 adds a `notifications_log` table with
exists-checks before send.

## Service-token rate-limit not enforced

`X-Service-Token`-authenticated callers bypass the per-IP rate-limit
middleware in v1.0 — there is no service-token bucket. This is fine for
trusted automation but means a misbehaving integration can hammer the
booking API. v1.1 introduces a per-token quota.

## Captcha is opt-in

`QOGNICAL_CAPTCHA_PROVIDER` is unset by default → the booking form has no
captcha. For public-internet deployments, set it to `hcaptcha` or
`turnstile` (Cloudflare). Recaptcha is intentionally not supported (it
ships Google tracking cookies which is hard to reconcile with DSGVO).

## Stripe refunds don't auto-flip booking status

The `charge.refunded` Stripe event does not carry `client_reference_id`
(only `checkout.session.*` does). qognical's webhook handler therefore
ignores refund events in v1.0. Hosts mark refunds manually in the
dashboard. v1.2 looks the booking up via `payment_external_id`.

## PayPal hold-mode not supported

Stripe supports authorise-now / capture-later via `capture_method=manual`,
which qognical exposes as `event_type.payment_mode=hold`. PayPal has an
equivalent (auth + later capture) but the UX is more involved; v1.0 only
supports `fixed`, `deposit`, and `open` for PayPal.

## Default rate-limits are generous

The defaults are tuned for "small team, internal traffic":

- Reads: 60/min per IP
- Mutations (POST/PUT/DELETE): 5/min per IP

For public-internet booking pages, tighten these via
`QOGNICAL_RATE_LIMIT_READS` and `QOGNICAL_RATE_LIMIT_MUTATIONS`. A
hard-DDoS-resilient setup needs a CDN or reverse-proxy WAF in front of
qognical regardless.

## No native multi-tenancy

One qognical instance ≠ one SaaS multi-tenant deployment. Each instance
has one Stripe secret, one set of email credentials, one logo. To host
multiple unrelated customers, run multiple instances. Multi-account
Stripe Connect is on the v2.0 roadmap.

---

If you hit one of these in production and want to fix it sooner than the
roadmap suggests, open a [GitHub issue](https://github.com/qognio/qognical/issues)
— most of the planned items are well-scoped and welcome PRs.

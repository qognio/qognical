# qognical roadmap

Living document. Tracks what's planned vs. what's done. Bug reports + feature
requests welcome via [GitHub issues](https://github.com/qognio/qognical/issues).

---

## v1.0 — current

Shipped:

- Single binary, single container, single SQLite volume
- Booking pipeline: validate → intake → price → reserve → notify
- Magic-link cancel + reschedule (HMAC-signed, single-use)
- iCal standard output (calendar invite + cancellation)
- Outbound webhooks (HMAC-SHA256, exponential backoff retry queue)
- Calendar adapters: Microsoft Graph, Google Calendar, Nextcloud
- Meeting providers: MS Teams, Google Meet, Zoom, Jitsi
- Payment providers: Stripe (Connect-ready), PayPal
- Captcha providers: hCaptcha, Cloudflare Turnstile
- DSGVO Art. 15 / 17 CLI commands (`export-data`, `delete-data`)
- Host self-service web console (HTML/JS, no framework)
- Embeddable booking page (`<iframe>` with origin allowlist)
- React wrapper package (source under `packages/embed-react/`)

---

## v1.1 — next

- `qognical webhooks deliver-now` CLI — for ops diagnostics, bypass the
  one-minute cron tick (good for smoke-tests + incident response)
- Service-token rate-limit bucket — currently service-tokens share the
  unauth-IP pool; v1.1 introduces a per-token quota
- Reminder log + idempotency — replace the 5-min cron-window match with
  a `notifications_log(booking_id, kind)` row gated by exists-check, so
  daemon restarts in the dispatch minute can't double-send
- Notifier retry-loop — fire-and-forget SMTP is currently best-effort;
  v1.1 wraps it in the same exponential-backoff queue as outbound webhooks
- `--credentials-stdin` flag on `integrations set` — avoid leaving a
  plaintext credentials JSON on disk during setup

---

## v1.2

- `users.slug` field with uniqueness + reserved-word allowlist — public
  booking URLs stop using email-as-slug (today's `/event-types/<email>/<slug>`
  trivially enumerates host emails)
- Granular per-action tokens — today a Cancel-token also authorizes View;
  v1.2 would issue separate tokens per email-link if operators want
  stricter authority (currently a deliberate UX trade-off, see KNOWN_LIMITATIONS)
- PayPal hold/capture-split — Stripe supports `capture_method=manual`
  in v1.0; PayPal's auth-then-capture flow needs UX work
- `charge.refunded` event handling — Stripe's refund event omits
  `client_reference_id`, so v1.0 needs operators to mark refunds
  manually; v1.2 adds a `payment_external_id`-based booking lookup

---

## v2.0 — speculative

- Multi-account Stripe Connect — operators with multiple legal entities
- Front-end framework migration — vanilla → Preact/Svelte if the SPA
  grows beyond what hand-rolled HTML+JS comfortably maintains
- Horizontal scale story — SQLite WAL caps practical throughput around
  ~1,000 bookings/day per instance; multi-instance + Postgres adapter
  is on the wish-list

---

Contributions to any of the above are welcome — open an issue first if it's
non-trivial so we can align on the approach.

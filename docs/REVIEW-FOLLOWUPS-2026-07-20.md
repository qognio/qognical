# Review-Follow-ups (Codex GPT-5.6-sol, 2026-07-20)

Zweiter unabhängiger Security-/Correctness-Review. Die meisten Findings sind in
diesem Commit behoben (siehe Commit-Message). Die folgenden werden **bewusst
aufgeschoben** — sie betreffen entweder auf den Live-Instanzen **nicht
aktivierte** Zahlungen oder sind größere Umbauten, die eigenes Testing brauchen.
Sie sind hier getrackt, damit sie nicht verloren gehen.

## Aufgeschoben — Zahlungen (auf termin./termine. NICHT konfiguriert)

- **HIGH `internal/pipeline/approval.go` — bezahlte Approval-Buchungen werden
  vor Zahlung als `confirmed` gespeichert.** `SetBookingApproval` setzt erst
  `confirmed`, dann versucht `HandleApproval` `confirmed → pending_payment` (von
  der State-Machine verboten, Fehler wird verschluckt). Fix: zahlungsabhängiges
  Ziel VOR der Approval-Transaktion bestimmen und atomar setzen; Checkout-Fehler
  nicht ignorieren.
- **HIGH `internal/adapters/paypal/paypal.go` — PayPal-One-Time ohne
  Capture-Pfad.** `CaptureOrder` hat keinen Aufrufer; nach `APPROVED` (jetzt
  korrekt nicht mehr als bezahlt gewertet) bleibt die Buchung in
  `pending_payment`. Fix: nach verifiziertem APPROVED idempotent `CaptureOrder`
  ausführen, erst danach `PAYMENT.CAPTURE.COMPLETED` bestätigen.
- **HIGH `internal/webhooks/inbound.go` — Webhook-Idempotenz race-anfällig +
  verlustbehaftet.** `HasProcessed`/`MarkProcessed` sind getrennt, Markierung
  vor Dispatch, Fehler ignoriert. Fix: Inbox-Tabelle mit Unique-Key
  `(provider,event_id)` und Zuständen `processing/succeeded/failed`; atomar
  claimen, erst nach Erfolg abschließen.

Bis Zahlungen aktiviert werden, sind diese Pfade nicht erreichbar (kein
`QOGNICAL_STRIPE_*` / `QOGNICAL_PAYPAL_*` gesetzt; `Config.Validate` erzwingt
fail-closed, sobald sie es sind).

## Aufgeschoben — größere Umbauten

- **HIGH `internal/api/api.go handleListSlots` — externe Kalender-Busy-Zeiten
  werden nicht berücksichtigt.** `FreeBusy` ist in MS-Graph/Google/Nextcloud
  implementiert, wird aber nirgends aufgerufen. Ein realer Kalendertermin des
  Hosts blockt weder Anzeige noch Buchung eines Slots. Fix: Provider auflösen,
  `FreeBusy` mit Timeout abrufen, als `ExternalBusy` einspeisen, klare
  fail-open/closed-Policy. Braucht Live-Test gegen echtes Graph → separater PR.
- **MEDIUM `internal/store/store.go ReplaceStartEnd` — Reschedule kennt
  Gruppen-Kapazität nicht.** Verschieben eines Teilnehmers in einen noch nicht
  vollen Gruppen-Slot wird wie ein Capacity-1-Konflikt abgelehnt. Fix:
  Event-Type/Capacity + Ziel-Session übergeben, denselben atomaren
  Capacity-Check wie `ReserveBookingTx` nutzen (Muster steht bereit).
- **HIGH `internal/spa/web/host-login.html` — Login postet an nicht
  registrierte Routen** (`/api/host/auth/password`, `oauth2-authorize`). Die
  Seite ist unter `/host/login` erreichbar, aber die Live-Hosts wurden per
  Superuser angelegt und nutzen sie nicht. Fix: Passwort-Login auf
  `/api/collections/users/auth-with-password` (PB v0.39) umstellen, OAuth-Flow
  korrekt implementieren, HTTP-Integrationstest.

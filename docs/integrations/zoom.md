# Zoom

Per-Host MeetingProvider via Server-to-Server-OAuth. Verfügbar ab v0.3.0.

## App-Setup

1. Auf https://marketplace.zoom.us als der Host einloggen.
2. **Develop → Build App → Server-to-Server OAuth** wählen.
3. Scopes setzen: mindestens `meeting:write:admin` (oder
   `meeting:write` für User-Level-Apps).
4. **Account-ID + Client-ID + Client-Secret** kopieren.
5. Die Zoom-User-ID = E-Mail des Zoom-Accounts dieses Hosts (z.B.
   `host@example.com`) bzw. die UID aus Zoom Settings.

## Credentials-Shape

```json
{
  "account_id":    "abcDEF12345",
  "client_id":     "...",
  "client_secret": "...",
  "user_id":       "host@example.com"
}
```

## Integration anlegen

```bash
docker compose exec qognical /qognical integrations set \
  --owner=<users.id> \
  --provider=zoom \
  --credentials-file=/tmp/zoom-credentials.json \
  --enable
rm /tmp/zoom-credentials.json
```

Anschließend Event-Type mit `location_type=online_zoom` anlegen.

## Endpoints

- Token-Refresh: `POST https://zoom.us/oauth/token?grant_type=account_credentials&account_id=...`
- Meeting erstellen: `POST https://api.zoom.us/v2/users/{user_id}/meetings`
- Meeting löschen: `DELETE https://api.zoom.us/v2/meetings/{meeting_id}`

Token wird in-memory gecached, 60 s vor Ablauf erneuert.

## Bekannte Stolpersteine

- **Rate-Limit pro App**: Zoom limitiert pro Endpoint. Beim Burst respektiert
  der Adapter `Retry-After`.
- **Meeting-Typ 2 (scheduled)** ist v0.3-Default. Instant-Meetings (Typ 1) sind
  kein Use-Case für ein Booking-Tool.
- **Waiting-Room**: standardmäßig deaktiviert (`waiting_room: false`). Wer das
  ändern will, patcht `internal/adapters/zoom/zoom.go::CreateMeeting`.
- **Account-Level vs. User-Level App**: für Multi-Host-Pools verwendet jeder
  Host eine eigene Server-to-Server-App in seinem Zoom-Workspace; eine zentrale
  App reicht nur wenn alle Hosts im selben Workspace sind.

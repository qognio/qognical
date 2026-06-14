# Jitsi

Zwei Betriebsmodi:

## Public-Modus

Konfiguration nur über `event_types.meeting_config`:

```json
{
  "base_url":    "https://meet.example.org",
  "room_prefix": "qognical-"
}
```

Adapter generiert deterministische Raum-URLs (`{base_url}/qognical-{booking_id}`),
keine Netzwerk-Calls beim Erstellen, `DeleteMeeting` ist No-Op.

## JWT-Modus

Für eigene Jitsi-Instanzen mit Token-Auth:

```json
{
  "base_url":      "https://meet.example.org",
  "app_id":        "qognical",
  "jwt_secret":    "supersecret-shared-with-jitsi",
  "jwt_algorithm": "HS256",
  "lifetime_min":  240
}
```

Adapter signiert ein HS256-JWT mit Raum-Claim und hängt es an die URL:
`?jwt=<token>`. JWT-Payload enthält `room`, `aud=jitsi`, `iss=app_id`,
`exp`, plus `context.user.{name,email}`.

`jwt_secret` ist via Env-Variable konfigurierbar
(`QOGNICAL_JITSI_JWT_SECRET`) oder pro Event-Type über `meeting_config`.
Master-Key ist nicht beteiligt — die Jitsi-Instanz muss denselben Secret
kennen.

## Bekannte Stolpersteine

- **Nur HS256** in v1.0. RS256 (mit Public-Key auf Jitsi-Seite) ist
  v1.x-Roadmap.
- **`exp`-Drift**: Token läuft nach `lifetime_min` ab. Lange Buchungen
  brauchen ggf. höheren Wert (Default 240 Min).

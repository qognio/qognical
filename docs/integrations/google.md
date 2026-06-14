# Google Calendar

Pro Host ein eigener OAuth-Consent (Delegated Auth). Kein Workspace-Admin-
Setup nötig (siehe ADR-0001).

## App-Registrierung

1. https://console.cloud.google.com → neues Projekt (z.B. `qognical-prod`).
2. **APIs & Services** → **Calendar API** aktivieren.
3. **OAuth Consent Screen**: External, Scope `.../auth/calendar`.
4. **Credentials** → **OAuth Client ID** (Web application). Authorized
   redirect URI: `https://<your-host>/oauth/google/callback`.

## Credentials-Shape

```json
{
  "client_id":     "...apps.googleusercontent.com",
  "client_secret": "...",
  "refresh_token": "1//abc...",
  "calendar_id":   "primary"
}
```

Der Host läuft beim Anlegen der Integration einmal durch den OAuth-Flow;
qognical persistiert das Refresh-Token verschlüsselt. Access-Tokens werden
in-memory gecached und 60 s vor Ablauf erneuert.

## Service-Account-Modus (server-to-server, ohne User-Consent)

Statt des OAuth-User-Flows kann ein Google-**Service-Account** verwendet werden.
Das ist die robustere Variante für einen reinen Server-Dienst: kein Consent-
Screen, kein ablaufendes Refresh-Token. Der Adapter signiert ein JWT (RS256)
und tauscht es per `jwt-bearer`-Grant (RFC 7523) gegen ein Access-Token.

```json
{
  "type":         "service_account",
  "private_key":  "-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----\n",
  "client_email": "name@project.iam.gserviceaccount.com",
  "private_key_id": "...",
  "token_uri":    "https://oauth2.googleapis.com/token",
  "calendar_id":  "owner@gmail.com"
}
```

Das ist exakt der Inhalt einer Google-SA-Key-JSON, ergänzt um `calendar_id`.

**Wichtig:** `calendar_id` MUSS die Adresse des freigegebenen Kalenders sein
(z. B. die Gmail des Eigentümers) — **nicht** `"primary"`, denn `primary` würde
den (leeren) Eigenkalender des Service-Accounts adressieren.

Setup:
1. SA + Key erzeugen (`gcloud iam service-accounts create …` + `keys create`).
2. Calendar API im SA-Projekt aktivieren.
3. Den Ziel-Kalender mit der `client_email` des SA teilen — Berechtigung
   „Termine verwalten" (für FreeBusy genügt Lesen, für Event-Anlage braucht es
   Schreibrechte). Bei Consumer-Konten (gmail.com) reicht das direkte Teilen;
   Domain-Wide-Delegation (`sub`) ist nicht nötig und wird hier nicht verwendet.

## Endpoints

- Auth: `POST https://oauth2.googleapis.com/token` (`grant_type=refresh_token`)
- FreeBusy: `POST /calendar/v3/freeBusy`
- Create: `POST /calendar/v3/calendars/{id}/events`
- Update: `PATCH /calendar/v3/calendars/{id}/events/{eventId}`
- Delete: `DELETE /calendar/v3/calendars/{id}/events/{eventId}`

## Google Meet (v0.3)

Wenn `event_type.location_type=online_google_meet` und der Host eine Google-
Calendar-Integration hat, hängt der Adapter beim Event-Anlegen automatisch
einen `conferenceData.createRequest` mit `hangoutsMeet` an. Google erzeugt
den Meet-Link und gibt ihn in `event.conferenceData.entryPoints[type=video]`
zurück — qognical speichert das in `bookings.meeting_join_url`. Kein
zusätzliches Scope nötig über das bereits gewährte `calendar`-Scope hinaus.

## Bekannte Stolpersteine

- **`invalid_grant`** beim Refresh = Refresh-Token wurde widerrufen (Host hat
  Passwort geändert oder App-Verbindung gelöscht). Adapter mappt das auf
  `ErrAuth`, qognical setzt `integrations.last_error`, Host muss neu
  autorisieren.
- **Quotas**: 1 Mio Queries/Tag/Projekt, 500 Queries/100s/User. Bei 403
  `rateLimitExceeded` → exponential backoff.
- **iCal-UID-Konflikte**: Wir geben Google die UID nicht extern vor; Google
  generiert eigene Event-IDs. Reschedule erfolgt per PATCH auf derselben ID.

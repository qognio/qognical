# MS Graph (Microsoft 365 / Exchange Online)

Aktiviert Kalender + Teams Online-Meetings (Doc 05 "Weg 1"). Pro Tenant ein
qognical-App-Registrierung, pro Host eine `integrations`-Zeile mit der
zugehörigen Mailbox-User-ID.

## Tenant-Setup

1. Im Azure-Portal **App registrations** → **New registration**:
   - Name: `qognical`
   - Supported account types: Single tenant
2. Unter **Certificates & secrets** → **New client secret** (12–24 Monate Laufzeit).
3. Unter **API permissions** → **Microsoft Graph** → **Application permissions**:
   - `Calendars.ReadWrite`
   - (kein zusätzlicher `OnlineMeetings.*` nötig für Weg 1)
4. **Grant admin consent** klicken.
5. **Application Access Policy** im Exchange Online setzen — Pflicht, damit
   die App nur auf qognical-Hosts zugreift, nicht auf alle Postfächer:

   ```powershell
   New-ApplicationAccessPolicy -AppId <client_id> `
     -PolicyScopeGroupId qognical-hosts@example.com `
     -AccessRight RestrictAccess `
     -Description "qognical booking integration"
   ```

## Credentials-Shape

`integrations.credentials` (JSON, vor Persistenz mit Master-Key verschlüsselt):

```json
{
  "tenant_id":     "00000000-0000-0000-0000-000000000000",
  "client_id":     "00000000-0000-0000-0000-000000000000",
  "client_secret": "...",
  "user_id":       "host@example.com"
}
```

`user_id` ist die Mailbox-User-ID (UPN oder Object-ID). Die User-ID muss im
Application-Access-Policy-Scope liegen.

## Endpoints

- Auth: `POST https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token`
- FreeBusy: `POST /v1.0/users/{user}/calendar/getSchedule`
- Create: `POST /v1.0/users/{user}/events` (mit `isOnlineMeeting=true`,
  `onlineMeetingProvider="teamsForBusiness"` für Teams-Meeting in einem Aufruf)
- Update: `PATCH /v1.0/users/{user}/events/{eventId}`
- Delete: `DELETE /v1.0/users/{user}/events/{eventId}`

## Bekannte Stolpersteine

- **getSchedule-Verzögerung**: Neu erstellte Events erscheinen 10-20 Min
  verzögert im Free/Busy-Resultat. **qognical betrachtet lokale `bookings`
  als Quelle der Wahrheit**, nicht Graph (INV-1).
- **Rate-Limits** pro App + pro Mailbox. Bei 429 respektiert der Adapter
  `Retry-After`.
- **Token-Refresh** läuft automatisch (Client-Credentials-Flow erzeugt jedes
  Mal neuen Access-Token, in-Memory gecached).

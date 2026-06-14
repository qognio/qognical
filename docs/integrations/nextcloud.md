# Nextcloud CalDAV

Pro Host ein **App-Passwort** (nicht das Hauptpasswort) — in Nextcloud unter
**Persönliche Einstellungen → Sicherheit** generieren, jederzeit widerrufbar.

## Credentials-Shape

```json
{
  "base_url":     "https://cloud.example.com",
  "username":     "alice",
  "app_password": "...",
  "calendar":     "personal"
}
```

`calendar` ist der Kalender-Slug (Standard "personal", andere bei mehreren
Kalendern via Nextcloud-UI).

## Endpoints (relativ zur Nextcloud-Instanz)

| Operation   | Methode  | URL                                                          |
|-------------|----------|--------------------------------------------------------------|
| FreeBusy    | `REPORT` | `/remote.php/dav/calendars/{user}/{calendar}/`               |
| Create      | `PUT`    | `/remote.php/dav/calendars/{user}/{calendar}/{uid}.ics`      |
| Update      | `PUT`    | `/remote.php/dav/calendars/{user}/{calendar}/{uid}.ics` (If-Match) |
| Delete      | `DELETE` | `/remote.php/dav/calendars/{user}/{calendar}/{uid}.ics`      |

Adapter signiert das ICS mit Origin "qognical" und schreibt jede Buchung in
genau eine `.ics`-Datei.

## Setup-Workflow

1. **In Nextcloud**: Persönliche Einstellungen → Sicherheit → App-Passwort
   erzeugen (Name z.B. `qognical`). App-Passwort wird genau einmal angezeigt.

2. **Plaintext-JSON in eine Datei** (wird beim CLI-Aufruf verschlüsselt):

   ```bash
   cat > /tmp/nc-credentials.json <<EOF
   {
     "base_url":     "https://cloud.example.com",
     "username":     "alice",
     "app_password": "xxxx-yyyy-zzzz",
     "calendar":     "personal"
   }
   EOF
   ```

3. **`integrations`-Record anlegen** (das CLI verschlüsselt mit dem
   Master-Key vor dem Persistieren):

   ```bash
   docker compose exec qognical /qognical integrations set \
     --owner=<users.id> \
     --provider=nextcloud \
     --credentials-file=/tmp/nc-credentials.json \
     --enable
   ```

4. **`/tmp/nc-credentials.json` sofort löschen** — die Datei enthielt das
   App-Passwort im Klartext.

5. **Smoke-Test**: eine Test-Buchung für diesen Host machen — der
   Kalendereintrag muss in Nextcloud erscheinen, Audit-Log zeigt
   `external_calendar_provider=nextcloud`.

## Bekannte Stolpersteine

- **App-Passwort widerrufen** → 401. Adapter mappt auf `ErrAuth`, Host muss
  ein neues App-Passwort generieren.
- **Andere CalDAV-Server** (Radicale, Baikal, iCloud) sind nicht getestet —
  qognical zielt explizit auf Nextcloud.
- **VTIMEZONE-Komplexität**: qognical schreibt ausschließlich UTC-Events
  (DTSTART/DTEND als `Z`-Zeit). Keine VTIMEZONE-Blöcke nötig, Nextcloud-Client
  konvertiert in lokale TZ.

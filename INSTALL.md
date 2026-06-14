# INSTALL

Setup-Guide für eine produktive qognical-Instanz auf einem Ubuntu-VPS
(`docker compose`-Pfad). Andere Distros / nacktes Binary in den
Abschnitten weiter unten.

## Voraussetzungen

- Docker 24+ und Docker Compose v2
- Eine Domain mit A-Record auf den Server (z.B. `book.example.com`)
- Mailserver-Zugang (SMTP host/user/pass)
- Reverse-Proxy für TLS-Termination (Caddy / Traefik / nginx)
- VPS mit min. 512 MB RAM, 10 GB Disk

## Schritt für Schritt

### 1. Encryption-Key generieren

Der Schlüssel verschlüsselt alle Provider-Credentials at rest. **Pflicht.**

```bash
docker run --rm ghcr.io/qognio/qognical:latest genkey
# → 32-byte-base64-string, sicher ablegen (Password-Manager o.ä.)
```

### 2. `.env` aufsetzen

```bash
mkdir /opt/qognical && cd /opt/qognical
curl -fsSL https://raw.githubusercontent.com/qognio/qognical/main/.env.example -o .env
$EDITOR .env
```

Pflicht-Werte (alle anderen optional):

```
QOGNICAL_BASE_URL=https://book.example.com
QOGNICAL_ENCRYPTION_KEY=<aus Schritt 1>
QOGNICAL_SMTP_HOST=smtp.example.com
QOGNICAL_SMTP_USER=...
QOGNICAL_SMTP_PASSWORD=...
QOGNICAL_SMTP_FROM=no-reply@example.com
```

### 3. `docker-compose.yml`

```yaml
services:
  qognical:
    image: ghcr.io/qognio/qognical:latest
    restart: unless-stopped
    env_file: .env
    volumes:
      - pb_data:/pb_data
    ports:
      - "8090:8090"

volumes:
  pb_data:
```

```bash
docker compose up -d
docker compose logs -f
```

Wenn `/healthz` 200 liefert, läuft sie:

```bash
curl http://localhost:8090/healthz
# {"status":"ok","version":"v1.0.0","components":{"db":"ok","schema":"ok"}}
```

### 4. Reverse-Proxy + TLS

**Caddy** (empfohlen — automatisches Let's Encrypt):

```caddy
book.example.com {
  reverse_proxy localhost:8090
}
```

**Traefik** (für vorhandene Stacks): Labels im Compose-File mit
`traefik.http.routers.qognical.rule=Host(\`book.example.com\`)` und
`traefik.http.routers.qognical.tls.certresolver=...`.

**nginx**:

```nginx
server {
  listen 443 ssl http2;
  server_name book.example.com;
  ssl_certificate     /etc/letsencrypt/live/book.example.com/fullchain.pem;
  ssl_certificate_key /etc/letsencrypt/live/book.example.com/privkey.pem;
  add_header Strict-Transport-Security "max-age=31536000" always;

  location / {
    proxy_pass http://localhost:8090;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
  }
}
```

### 5. Ersten Superuser anlegen

Beim ersten `/_/`-Aufruf erscheint der Setup-Wizard von PocketBase. Alternativ
per CLI:

```bash
docker compose exec qognical /qognical superuser upsert admin@example.com 'STRONG_PASSWORD'
```

### 6. Ersten Host anlegen

In `/_/` unter `Collections → users`:

| Feld | Wert |
|---|---|
| email | host@example.com |
| password | (sicher generiert) |
| name | Vorname Nachname |
| timezone | Europe/Berlin |
| role | host |

### 7. Event-Type + Verfügbarkeit anlegen

`Collections → event_types → New record`:

```json
{
  "owner": "<users.id>",
  "slug": "erstgespraech",
  "title": "30-Min Erstgespräch",
  "duration_min": 30,
  "min_notice_min": 60,
  "max_horizon_days": 30,
  "location_type": "none",
  "payment_mode": "none",
  "schema_version": 1,
  "active": true,
  "intake_schema": {"fields": []}
}
```

Plus `availability`-Einträge: Mo-Fr `start=09:00 end=17:00 weekday=0..4`.

Booking-URL danach: `https://book.example.com/book/<host-email>/erstgespraech`.

## Updates

```bash
cd /opt/qognical
docker compose pull
docker compose up -d
```

Migrationen laufen automatisch beim Container-Start. Backup vor jedem
größeren Update: `docker compose exec qognical /qognical backup` (in v1.1)
oder per `docker run --rm -v qognical_pb_data:/data alpine tar czf - /data > backup.tgz`.

Mehr in [`UPGRADING.md`](UPGRADING.md).

## Backup

Alles steckt in `/pb_data`. Empfehlung: `restic` pull-basiert auf S3/B2,
verschlüsselt, off-site. Beispiel-Cron in `docs/ops/backup-example.cron`.

## Bare-Binary (ohne Docker)

```bash
curl -L https://github.com/qognio/qognical/releases/latest/download/qognical-linux-amd64.tar.gz | tar xz
sudo mv qognical /usr/local/bin/

# .env wie oben, dann:
export $(cat /opt/qognical/.env | xargs)
qognical serve --http=0.0.0.0:8090 --dir=/var/lib/qognical
```

systemd-Unit unter `docs/ops/qognical.service`.

## Troubleshooting

| Symptom | Ursache |
|---|---|
| `missing required env vars` beim Start | Pflicht-Vars fehlen, siehe Schritt 2 |
| `QOGNICAL_ENCRYPTION_KEY invalid` | Key ist keine 32-byte-base64 — neu generieren |
| `/healthz` → 503 mit `schema:fail` | Migration unvollständig; Container neu starten |
| Bestätigungs-Mails kommen nicht an | SMTP-Daten prüfen; `LOG_LEVEL=debug` setzen + Container-Log lesen |
| Webhook von Stripe → "signature mismatch" | `QOGNICAL_STRIPE_WEBHOOK_SECRET` stimmt nicht mit dem im Stripe-Dashboard überein |

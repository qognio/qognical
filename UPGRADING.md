# UPGRADING

## Allgemeiner Update-Pfad

qognical folgt Semantic Versioning:

- **Patch** (v1.0.0 → v1.0.1): Bugfixes, sicher zu aktualisieren.
- **Minor** (v1.0.x → v1.1.0): neue Features, abwärtskompatibel.
- **Major** (v1.x → v2.0.0): Breaking Changes — manuelle Schritte möglich,
  in diesem Dokument pro Version aufgeführt.

### Standard-Update (Patch + Minor)

```bash
cd /opt/qognical

# 1. Backup
docker run --rm -v qognical_pb_data:/data alpine \
  tar czf - /data > backup-$(date +%F).tgz

# 2. Pull + restart
docker compose pull
docker compose up -d

# 3. Migrations laufen automatisch beim Container-Start
docker compose logs --tail=50 qognical | grep migrat

# 4. Healthcheck
curl https://book.example.com/healthz
```

Bei Migrations-Fehler: alter Container automatisch nicht-healthy
(Docker-HEALTHCHECK), Restart-Loop. Logs prüfen, dann:

```bash
# Rollback
docker compose down
docker run --rm -v qognical_pb_data:/data alpine sh -c 'rm -rf /data/*'
docker run --rm -v qognical_pb_data:/data -v $PWD:/host alpine \
  tar xzf /host/backup-<date>.tgz -C /
docker compose up -d  # mit altem Image-Tag
```

## Versions-spezifische Notizen

## Versions-Schema

Solange `v0.x.y`: API + Schema können in Minor-Bumps unangekündigte
Breaking Changes haben. `v1.0.0` wird das erste Release nach echtem
Produktiv-Einsatz und einer formellen API-Freeze-Periode.

### v0.1.0 (Foundation Release)

Erstes intern stabiles Release nach Phase 1-5 der Planungs-Docs. Falls du
aus einem Pre-v0.1-Snapshot upgradest:

- Collection-Namen sind stabil: `users`, `event_types`, `availability`,
  `date_overrides`, `bookings`, `integrations`, `service_tokens`,
  `notifications_log`, `outbound_webhooks`, `webhook_deliveries`, `audit_log`.
- Falls du händisch Collections angelegt hast, vor v1.0 löschen oder umbenennen
  (Schema-Konflikt).
- Schema-Migration `1730000000_initial_schema` erweitert die default `users`-
  Collection um `timezone` und `role` — bestehende User bekommen leere Felder
  und müssen im Admin manuell ergänzt werden.

## LTS-Politik

Jede Major-Version bekommt **mindestens 6 Monate Security-Fixes nach Release
der nächsten Major-Version**. Bei v2.0.0-Release wird v1.x parallel weiter
mit Sicherheits-Patches versorgt.

## Migrations-Mechanik

Migrations sind Go-Code im Repo unter `migrations/`. Beim Container-Start
liest PocketBase die `_migrations`-Tabelle, sieht welche `up`-Funktionen
schon angewandt wurden, und führt die ausstehenden in der Reihenfolge ihrer
Dateinamen-Timestamps aus.

**Idempotenz**: jede Migration ist so geschrieben, dass das mehrfache
Ausführen kein Problem darstellt (Schema-Check vor Anlegen, INSERT IGNORE etc.).

**Rückwärtskompatibilität**: jede `up`-Migration hat eine zugehörige `down`,
die für mindestens eine Minor-Version rückwärts ausgeführt werden kann.

**Schema-Version-Konsistenz**: Healthcheck (`/healthz`) prüft alle erwarteten
Collections — bei Mismatch HTTP 503 mit `components.schema=fail`. Reverse-Proxy
kann den Traffic abdrehen bis das Update durch ist.

## Backup-Test

Vor jedem Major-Update empfohlen:

```bash
# Restore-Test auf Staging
docker volume create qognical_pb_data_staging
docker run --rm -v qognical_pb_data_staging:/data -v $PWD:/host alpine \
  tar xzf /host/backup-<date>.tgz -C /
docker run -d --name qognical-staging --env-file .env \
  -v qognical_pb_data_staging:/pb_data -p 18099:8090 \
  ghcr.io/qognio/qognical:latest

# Verify
curl http://localhost:18099/healthz
```

Wenn der Restore funktioniert: Production-Update wagen.

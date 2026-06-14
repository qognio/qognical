#!/usr/bin/env bash
# Seed a fresh qognical instance with one host + three paid event-types
# (fixed/deposit/hold) + Mon-Fri 09-17 availability. Used by smoke-stripe.py.
#
# Required env:
#   QOGNICAL_URL          (default http://127.0.0.1:18099)
#   QOGNICAL_ADMIN_EMAIL  (default admin@test.local)
#   QOGNICAL_ADMIN_PASSWORD
set -euo pipefail

BASE="${QOGNICAL_URL:-http://127.0.0.1:18099}"
EMAIL="${QOGNICAL_ADMIN_EMAIL:-admin@test.local}"
PW="${QOGNICAL_ADMIN_PASSWORD:?Set QOGNICAL_ADMIN_PASSWORD}"

TOKEN=$(curl -sS -X POST "$BASE/api/collections/_superusers/auth-with-password" \
  -H 'Content-Type: application/json' \
  -d "{\"identity\":\"$EMAIL\",\"password\":\"$PW\"}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")

HOST_ID=$(curl -sS -X POST "$BASE/api/collections/users/records" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"email":"host@example.com","password":"HostPass1234","passwordConfirm":"HostPass1234","name":"Host","timezone":"Europe/Berlin","role":"host"}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
echo "host: $HOST_ID"

for body in \
  "{\"owner\":\"$HOST_ID\",\"slug\":\"fixed\",\"title\":\"Fixed 120€\",\"duration_min\":30,\"min_notice_min\":60,\"max_horizon_days\":30,\"location_type\":\"none\",\"payment_mode\":\"fixed\",\"payment_amount\":12000,\"payment_currency\":\"EUR\",\"payment_provider\":\"stripe\",\"schema_version\":1,\"active\":true,\"intake_schema\":{\"fields\":[{\"key\":\"topic\",\"label\":\"x\",\"type\":\"text\",\"required\":true}]}}" \
  "{\"owner\":\"$HOST_ID\",\"slug\":\"deposit\",\"title\":\"Deposit 50€\",\"duration_min\":60,\"min_notice_min\":60,\"max_horizon_days\":30,\"location_type\":\"none\",\"payment_mode\":\"deposit\",\"payment_amount\":5000,\"payment_currency\":\"EUR\",\"payment_provider\":\"stripe\",\"schema_version\":1,\"active\":true}" \
  "{\"owner\":\"$HOST_ID\",\"slug\":\"hold\",\"title\":\"Hold 80€\",\"duration_min\":45,\"min_notice_min\":60,\"max_horizon_days\":30,\"location_type\":\"none\",\"payment_mode\":\"hold\",\"payment_amount\":8000,\"payment_currency\":\"EUR\",\"payment_provider\":\"stripe\",\"schema_version\":1,\"active\":true}"
do
  curl -sS -X POST "$BASE/api/collections/event_types/records" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "$body" | python3 -c "import sys,json;d=json.load(sys.stdin);print(' ',d.get('slug','?'),'->',d.get('id','?'))"
done

for wd in 0 1 2 3 4; do
  curl -sS -X POST "$BASE/api/collections/availability/records" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"owner\":\"$HOST_ID\",\"weekday\":$wd,\"start\":\"09:00\",\"end\":\"17:00\"}" >/dev/null
done
echo "  availability Mon–Fri 09-17 seeded"

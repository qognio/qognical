#!/usr/bin/env python3
"""Security-Test-Suite for docs/planning/09 SEC-1 through SEC-12.

Boot qognical separately (see scripts/seed.sh) and point env at it:

  QOGNICAL_URL=http://127.0.0.1:18099
  QOGNICAL_ADMIN_PASSWORD=AdminPass123
  STRIPE_WEBHOOK_SECRET=whsec_...
  QOGNICAL_BIN=/tmp/qognical
  python3 scripts/sec-tests.py

Some checks are also (and more reliably) covered by Go unit tests:
  SEC-7  internal/token: TestSingleUseRotation
  SEC-9  internal/crypto: roundtrip + plaintext-absence
  SEC-5  internal/adapters/stripe: TestWebhookRejectsTamper
  SEC-6  internal/adapters/stripe: TestWebhookValidSignature + replay test
This script verifies end-to-end behavior across the live HTTP surface.
"""
import hmac, hashlib, json, os, subprocess, sys, time, urllib.parse, urllib.request, urllib.error

BASE = os.environ.get("QOGNICAL_URL", "http://127.0.0.1:18099")
ADMIN_EMAIL = os.environ.get("QOGNICAL_ADMIN_EMAIL", "admin@test.local")
ADMIN_PW = os.environ["QOGNICAL_ADMIN_PASSWORD"]
WHSEC = os.environ.get("STRIPE_WEBHOOK_SECRET", "").encode()
HOST_SLUG = os.environ.get("HOST_SLUG", "host@example.com")
QOGNICAL_BIN = os.environ.get("QOGNICAL_BIN", "/tmp/qognical")


def quote(s: str) -> str:
    return urllib.parse.quote(s, safe="")


def req(method, url, data=None, headers=None):
    body = data if isinstance(data, (bytes, type(None))) else json.dumps(data).encode()
    r = urllib.request.Request(url, data=body, method=method,
        headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(r, timeout=10) as resp:
            ctype = resp.headers.get("Content-Type", "")
            raw = resp.read()
            return resp.status, dict(resp.getheaders()), (json.loads(raw or b"{}") if "json" in ctype else raw)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, dict(e.headers.items()), json.loads(raw)
        except Exception:
            return e.code, dict(e.headers.items()), raw.decode(errors="replace")


def post(url, data, headers=None): return req("POST", url, data, headers)
def get(url, headers=None):        return req("GET",  url, None, headers)


code, _, login = post(f"{BASE}/api/collections/_superusers/auth-with-password",
                       {"identity": ADMIN_EMAIL, "password": ADMIN_PW})
ADMIN = login["token"]
HDR = {"Authorization": f"Bearer {ADMIN}"}

R = []  # (name, ok | None=skipped, detail)


# SEC-1: Public event-type endpoint must not leak host_email.
_, _, et = get(f"{BASE}/api/public/v1/event-types/{quote(HOST_SLUG)}/fixed")
ok = isinstance(et, dict) and "host_email" not in et
R.append(("SEC-1 no host email in public event-type", ok,
          list(et.keys()) if isinstance(et, dict) else et))


# SEC-2: Public booking GET requires token + cannot read foreign booking.
# Pick any booking id from admin view (if any), then try without token.
code, _, books = get(f"{BASE}/api/collections/bookings/records?perPage=1", HDR)
if isinstance(books, dict) and books.get("items"):
    bid = books["items"][0]["id"]
    code, _, body = get(f"{BASE}/api/public/v1/bookings/{bid}")
    R.append(("SEC-2 booking endpoint refuses without token",
              code in (401, 403),
              {"code": code, "body": body}))
else:
    R.append(("SEC-2 booking endpoint refuses without token", None, "no booking seeded"))


# SEC-3: CORS doesn't mirror arbitrary Origin.
_, headers, _ = get(f"{BASE}/api/public/v1/event-types/{quote(HOST_SLUG)}/fixed",
                    {"Origin": "https://evil.example"})
acao = headers.get("Access-Control-Allow-Origin", "")
R.append(("SEC-3 CORS rejects non-allowlisted Origin",
          acao == "" or acao == "*" and False, {"acao": acao}))


# SEC-4: rate-limit fires on burst (default 60/min reads).
codes = []
for _ in range(80):
    c, _, _ = get(f"{BASE}/api/public/v1/event-types/{quote(HOST_SLUG)}/fixed")
    codes.append(c)
blocked = sum(1 for c in codes if c == 429)
R.append(("SEC-4 burst triggers 429",
          blocked > 0,
          {"ok": codes.count(200), "blocked": blocked}))


# SEC-5: Webhook without signature → 400
code, _, _ = post(f"{BASE}/webhooks/stripe",
                  b'{"id":"evt","type":"checkout.session.completed","created":0,"data":{}}',
                  {"Stripe-Signature": ""})
R.append(("SEC-5 webhook without signature rejected", code == 400, code))


# SEC-6: idempotent replay
if WHSEC:
    time.sleep(1)
    ts = str(int(time.time()))
    event_id = f"evt_sec_{ts}"
    body = json.dumps({"id": event_id, "type": "checkout.session.completed",
                       "created": int(ts), "data": {"object": {"id": "cs_x"}}},
                      separators=(",", ":"))
    sig = hmac.new(WHSEC, (ts + "." + body).encode(), hashlib.sha256).hexdigest()
    h = {"Stripe-Signature": f"t={ts},v1={sig}"}
    post(f"{BASE}/webhooks/stripe", body.encode(), h)
    code2, _, body2 = post(f"{BASE}/webhooks/stripe", body.encode(), h)
    R.append(("SEC-6 webhook idempotent replay → duplicate",
              isinstance(body2, dict) and body2.get("status") == "duplicate", body2))
else:
    R.append(("SEC-6 webhook idempotent replay → duplicate", None, "skipped (no STRIPE_WEBHOOK_SECRET)"))


# SEC-8: XSS in input not echoed verbatim in error response (Doc 09 FRM-4)
xss = '<script>alert(1)</script>'
code, _, body = post(f"{BASE}/api/public/v1/bookings",
    {"event_type_id": "nonexistent", "start_utc": "2026-12-01T10:00:00Z",
     "invitee": {"name": xss, "email": "x@y.test", "timezone": "Europe/Berlin"},
     "intake_data": {"topic": xss}})
serialized = json.dumps(body) if not isinstance(body, str) else body
echoed_raw = "<script>" in serialized
R.append(("SEC-8 raw XSS payload not echoed in error", not echoed_raw, code))


# SEC-9: integrations.credentials never plaintext via PB API
_, _, integrations = get(f"{BASE}/api/collections/integrations/records", HDR)
items = integrations.get("items", []) if isinstance(integrations, dict) else []
contains = any(("sk_test_" in str(it) or "client_secret\":\"" in str(it)) for it in items)
R.append(("SEC-9 no plaintext credentials in PB API", not contains, len(items)))


# SEC-10: audit_log refuses non-superuser writes
code, _, _ = post(f"{BASE}/api/collections/audit_log/records",
                   {"actor": "anon", "action": "test"})
R.append(("SEC-10 audit_log refuses anonymous writes", code in (401, 403), code))


# SEC-12: brute-force lockout (PB default 5 fails → 429)
# Use a non-existent email to avoid locking out real users for the test.
codes = []
for _ in range(7):
    c, _, _ = post(f"{BASE}/api/collections/_superusers/auth-with-password",
                    {"identity": "doesnotexist@invalid.local", "password": "WRONG"})
    codes.append(c)
R.append(("SEC-12 brute-force lockout fires", 429 in codes or 423 in codes,
          {"codes": codes}))


# SEC-11: server refuses to start without encryption key
env = {k: v for k, v in os.environ.items() if not k.startswith("QOGNICAL_")}
env.update({"QOGNICAL_BASE_URL": "http://x", "QOGNICAL_SMTP_HOST": "x",
            "QOGNICAL_SMTP_USER": "x", "QOGNICAL_SMTP_PASSWORD": "x",
            "QOGNICAL_SMTP_FROM": "x"})
try:
    out = subprocess.run([QOGNICAL_BIN, "serve"], env=env,
                         capture_output=True, timeout=5, text=True)
    combined = (out.stdout or "") + (out.stderr or "")
    ok = out.returncode != 0 and "ENCRYPTION_KEY" in combined.upper()
    R.append(("SEC-11 server refuses start w/o encryption key", ok,
              {"rc": out.returncode, "msg": combined[:200]}))
except FileNotFoundError:
    R.append(("SEC-11 server refuses start w/o encryption key", None,
              f"binary {QOGNICAL_BIN} not found"))
except subprocess.TimeoutExpired:
    R.append(("SEC-11 server refuses start w/o encryption key", False,
              "timeout — process did NOT exit on missing key"))


# ----- summary -----
print(f"\n{'TEST':60} OK  DETAIL")
print("-" * 130)
allpass = True
skipped = 0
for name, ok, detail in R:
    mark = "✓" if ok is True else ("·" if ok is None else "✗")
    if ok is None: skipped += 1
    elif not ok: allpass = False
    print(f"{name:60} {mark}   {detail}")
print()
print(f"OVERALL: {'ALL PASS ✓' if allpass else '✗ FAILURES'}"
      f"{f' ({skipped} skipped)' if skipped else ''}")
sys.exit(0 if allpass else 1)

#!/usr/bin/env python3
"""Phase-3 Stripe smoke against a real test/sandbox account.

Prereqs:
  - qognical running locally on $QOGNICAL_URL (default http://127.0.0.1:18099),
    booted with QOGNICAL_STRIPE_SECRET_KEY + QOGNICAL_STRIPE_WEBHOOK_SECRET set
  - QOGNICAL_ADMIN_EMAIL + QOGNICAL_ADMIN_PASSWORD env (superuser)
  - STRIPE_SECRET_KEY + STRIPE_WEBHOOK_SECRET env (matching the qognical config)
  - A host with the three event-type slugs "fixed", "deposit", "hold" + Mon-Fri
    09-17 availability seeded under host@example.com (see scripts/seed.sh).

Covers:
  UC-2 fixed                  checkout.session.completed → confirmed
  UC-3 deposit                partial amount → confirmed
  UC-4 hold                   session created (capture_method invisible on
                              open sessions, see internal/adapters/stripe
                              unit test for form-body verification)
  Async SEPA path             checkout.session.async_payment_succeeded
  E2E-7 idempotency           event_id replay → duplicate
  Expired                     checkout.session.expired → abandoned
  Tamper                      bad signature → 400
  Replay window               ±5min enforced
"""
import hmac, hashlib, json, os, sys, time, urllib.request, urllib.error

BASE = os.environ.get("QOGNICAL_URL", "http://127.0.0.1:18099")
WHSEC = os.environ["STRIPE_WEBHOOK_SECRET"].encode()
SKEY = os.environ["STRIPE_SECRET_KEY"]
ADMIN_EMAIL = os.environ.get("QOGNICAL_ADMIN_EMAIL", "admin@test.local")
ADMIN_PW    = os.environ["QOGNICAL_ADMIN_PASSWORD"]
HOST_SLUG   = os.environ.get("HOST_SLUG", "host@example.com")

def req(method, url, data=None, headers=None):
    body = data if isinstance(data, (bytes, type(None))) else json.dumps(data).encode()
    r = urllib.request.Request(url, data=body, method=method,
        headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(r, timeout=15) as resp:
            return resp.status, json.loads(resp.read() or b'{}')
    except urllib.error.HTTPError as e:
        try: return e.code, json.loads(e.read())
        except Exception: return e.code, None

post = lambda u, d, h=None: req("POST", u, d, h)
get  = lambda u, h=None:    req("GET",  u, None, h)[1]

token = post(f"{BASE}/api/collections/_superusers/auth-with-password",
    {"identity": ADMIN_EMAIL, "password": ADMIN_PW})[1]["token"]
HDR = {"Authorization": f"Bearer {token}"}

def lookup_event_types():
    # Find the three slugs we need.
    out = {}
    for slug in ("fixed", "deposit", "hold"):
        d = get(f"{BASE}/api/public/v1/event-types/{urllib_quote(HOST_SLUG)}/{slug}")
        out[slug] = d["id"]
    return out

def urllib_quote(s):
    return urllib.request.quote(s, safe="")

EVTS = lookup_event_types()
INTAKE = {"fixed": {"topic": "smoke"}, "deposit": {}, "hold": {}}

# wipe existing bookings before run
for it in get(f"{BASE}/api/collections/bookings/records?perPage=200", HDR).get("items", []):
    req("DELETE", f"{BASE}/api/collections/bookings/records/{it['id']}", headers=HDR)

def slots_of(slug, frm="2026-06-01", to="2026-06-20"):
    return get(f"{BASE}/api/public/v1/event-types/{urllib_quote(HOST_SLUG)}/{slug}/slots"
               f"?from={frm}&to={to}&timezone=Europe/Berlin")["slots"]

def book(slug, slot_idx, name):
    s = slots_of(slug)[slot_idx]["start_utc"]
    code, res = post(f"{BASE}/api/public/v1/bookings", {
        "event_type_id": EVTS[slug], "start_utc": s,
        "invitee": {"name": name, "email": f"{name.lower()}@invitee.test",
                    "timezone": "Europe/Berlin"},
        "intake_data": INTAKE[slug]})
    if "booking_id" not in res:
        raise SystemExit(f"book({slug}, {slot_idx}) failed: {code} {res}")
    return res

def webhook(event_type, booking_id, session_id, amount, event_id):
    ts = str(int(time.time()))
    body = json.dumps({"id": event_id, "type": event_type, "created": int(ts),
        "data": {"object": {"id": session_id, "client_reference_id": booking_id,
            "amount_total": amount, "currency": "eur", "payment_status": "paid"}}},
        separators=(",", ":"))
    sig = hmac.new(WHSEC, (ts + "." + body).encode(), hashlib.sha256).hexdigest()
    return post(f"{BASE}/webhooks/stripe", body.encode(),
                {"Stripe-Signature": f"t={ts},v1={sig}"})

def state(bid):
    d = get(f"{BASE}/api/collections/bookings/records/{bid}", HDR)
    return {k: d.get(k) for k in ["status", "payment_status",
                                    "payment_external_id", "payment_amount_paid"]}

def sid_from_checkout(url):
    return url.split("/c/pay/")[1].split("#")[0]

R = []

# UC-2 fixed
r = book("fixed", 0, "Anna")
webhook("checkout.session.completed", r["booking_id"], sid_from_checkout(r["checkout_url"]),
        12000, f"evt_uc2_{int(time.time()*1000)}")
time.sleep(2)
s = state(r["booking_id"])
R.append(("UC-2 fixed (120€) → confirmed",
          s["status"] == "confirmed" and s["payment_amount_paid"] == 12000, s))

# UC-3 deposit
r = book("deposit", 5, "Bert")
webhook("checkout.session.completed", r["booking_id"], sid_from_checkout(r["checkout_url"]),
        5000, f"evt_uc3_{int(time.time()*1000)}")
time.sleep(2)
s = state(r["booking_id"])
R.append(("UC-3 deposit (50€) → confirmed",
          s["status"] == "confirmed" and s["payment_amount_paid"] == 5000, s))

# UC-4 hold — only session-shape verifiable here (see module docstring).
r = book("hold", 15, "Carla")
sid = sid_from_checkout(r["checkout_url"])
session = get(f"https://api.stripe.com/v1/checkout/sessions/{sid}",
              {"Authorization": f"Bearer {SKEY}"})
R.append(("UC-4 hold: session created (mode=payment, amount=80€)",
          session.get("mode") == "payment" and session.get("amount_total") == 8000,
          {"mode": session.get("mode"), "amount_total": session.get("amount_total")}))

# Async SEPA
r = book("fixed", 1, "Eva")
webhook("checkout.session.async_payment_succeeded", r["booking_id"],
        sid_from_checkout(r["checkout_url"]),
        12000, f"evt_async_{int(time.time()*1000)}")
time.sleep(2)
s = state(r["booking_id"])
R.append(("Async SEPA succeeded → confirmed", s["status"] == "confirmed", s))

# Idempotency
ev = f"evt_dup_{int(time.time()*1000)}"
webhook("checkout.session.completed", "x", "cs_dup", 1000, ev)
time.sleep(1)
_, body = webhook("checkout.session.completed", "x", "cs_dup", 1000, ev)
R.append(("E2E-7 idempotent replay → duplicate",
          body == {"status": "duplicate"}, body))

# Expired
r = book("fixed", 20, "Frieda")
webhook("checkout.session.expired", r["booking_id"], sid_from_checkout(r["checkout_url"]),
        12000, f"evt_exp_{int(time.time()*1000)}")
time.sleep(2)
s = state(r["booking_id"])
R.append(("Expired → abandoned", s["status"] == "abandoned", s))

# Tamper
ts = str(int(time.time()))
code, _ = post(f"{BASE}/webhooks/stripe",
    b'{"id":"evt_t","type":"checkout.session.completed","created":0,"data":{}}',
    {"Stripe-Signature": f"t={ts},v1=00deadbeef"})
R.append(("Tamper rejected (400)", code == 400, code))

# Replay window
old_ts = str(int(time.time()) - 600)
body = json.dumps({"id": "evt_old", "type": "checkout.session.completed",
                   "created": int(old_ts), "data": {"object": {}}},
                  separators=(",", ":"))
sig = hmac.new(WHSEC, (old_ts + "." + body).encode(), hashlib.sha256).hexdigest()
code, _ = post(f"{BASE}/webhooks/stripe", body.encode(),
               {"Stripe-Signature": f"t={old_ts},v1={sig}"})
R.append(("Replay window: 10-min-old ts rejected (400)", code == 400, code))

print(f"\n{'TEST':50}  OK  DETAIL")
print("-" * 120)
allpass = True
for name, ok, detail in R:
    print(f"{name:50}  {'✓' if ok else '✗'}   {detail}")
    if not ok: allpass = False
print()
print("OVERALL:", "ALL PASS ✓" if allpass else "✗ FAILURES")
sys.exit(0 if allpass else 1)

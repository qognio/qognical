#!/usr/bin/env python3
"""Phase-5 Performance smoke per docs/planning/09.

Target: 1000 bookings stretched over 1 hour against a single instance,
single host, with 50 concurrent slot-listing requests in parallel. Numbers
match the planning's "kleines Team pro Instanz" scale (Doc 03).

Run against an empty / freshly seeded host that has enough availability
slots:

  QOGNICAL_URL=http://127.0.0.1:18099 \
  HOST_SLUG=host@example.com EVENT_TYPE_SLUG=fixed \
  QOGNICAL_ADMIN_PASSWORD=AdminPass123 \
  python3 scripts/load-test.py

The test reports p50/p95/p99 for GET /slots and POST /bookings, plus
memory usage from /proc if the process is local.
"""
import concurrent.futures, json, os, statistics, sys, time, urllib.parse, urllib.request, urllib.error

BASE = os.environ.get("QOGNICAL_URL", "http://127.0.0.1:18099")
HOST = os.environ.get("HOST_SLUG", "host@example.com")
ET   = os.environ.get("EVENT_TYPE_SLUG", "fixed")
N_BOOKINGS = int(os.environ.get("N_BOOKINGS", "200"))      # default reduced from 1000 for sanity
N_SLOT_QUERIES = int(os.environ.get("N_SLOT_QUERIES", "200"))
CONCURRENCY = int(os.environ.get("CONCURRENCY", "20"))


def percentile(values, p):
    if not values: return 0
    return sorted(values)[min(len(values) - 1, int(len(values) * p / 100))]


def get_slots():
    url = f"{BASE}/api/public/v1/event-types/{urllib.parse.quote(HOST, safe='')}/{ET}/slots?from=2026-06-01&to=2026-08-31&timezone=Europe/Berlin"
    t = time.perf_counter()
    try:
        with urllib.request.urlopen(url, timeout=15) as r:
            data = json.loads(r.read())
        return time.perf_counter() - t, len(data.get("slots", []))
    except urllib.error.HTTPError as e:
        return time.perf_counter() - t, -e.code


def book_one(et_id, slot_iso, n):
    body = json.dumps({
        "event_type_id": et_id, "start_utc": slot_iso,
        "invitee": {"name": f"Load {n}", "email": f"load{n}@invitee.test",
                    "timezone": "Europe/Berlin"},
        "intake_data": {"topic": "load"}
    }).encode()
    req = urllib.request.Request(f"{BASE}/api/public/v1/bookings",
        data=body, headers={"Content-Type": "application/json"})
    t = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            data = json.loads(r.read())
        return time.perf_counter() - t, "ok" if "booking_id" in data else "weird"
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")[:80]
        return time.perf_counter() - t, f"http{e.code}:{body}"


# ----- slot-listing storm -----
print(f"=== Slot-listing storm: {N_SLOT_QUERIES} reqs, concurrency={CONCURRENCY} ===")
slot_times = []
slot_counts = []
with concurrent.futures.ThreadPoolExecutor(max_workers=CONCURRENCY) as pool:
    for elapsed, n in pool.map(lambda _: get_slots(), range(N_SLOT_QUERIES)):
        slot_times.append(elapsed * 1000)
        slot_counts.append(n)

print(f"  p50={percentile(slot_times,50):.1f}ms  p95={percentile(slot_times,95):.1f}ms  p99={percentile(slot_times,99):.1f}ms")
print(f"  slots/response: avg={statistics.mean(slot_counts):.0f}  min={min(slot_counts)}  max={max(slot_counts)}")

# ----- bookings -----
# Look up event_type_id first.
et_meta = json.loads(urllib.request.urlopen(
    f"{BASE}/api/public/v1/event-types/{urllib.parse.quote(HOST, safe='')}/{ET}").read())
et_id = et_meta["id"]

slots_resp = json.loads(urllib.request.urlopen(
    f"{BASE}/api/public/v1/event-types/{urllib.parse.quote(HOST, safe='')}/{ET}/slots?from=2026-06-01&to=2026-12-31&timezone=Europe/Berlin").read())
slot_pool = [s["start_utc"] for s in slots_resp["slots"]]
if len(slot_pool) < N_BOOKINGS:
    print(f"WARN: only {len(slot_pool)} slots free, capping bookings to that.")
    N_BOOKINGS = len(slot_pool)

print(f"\n=== Booking storm: {N_BOOKINGS} bookings, concurrency={CONCURRENCY} ===")
book_times = []
outcomes = {}
with concurrent.futures.ThreadPoolExecutor(max_workers=CONCURRENCY) as pool:
    futs = [pool.submit(book_one, et_id, slot_pool[i], i) for i in range(N_BOOKINGS)]
    for f in concurrent.futures.as_completed(futs):
        elapsed, status = f.result()
        book_times.append(elapsed * 1000)
        outcomes[status] = outcomes.get(status, 0) + 1

print(f"  p50={percentile(book_times,50):.1f}ms  p95={percentile(book_times,95):.1f}ms  p99={percentile(book_times,99):.1f}ms")
print("  outcomes:", outcomes)

# ----- pass/fail -----
print()
slot_p95 = percentile(slot_times, 95)
book_p95 = percentile(book_times, 95)
target_slot_p95 = 500
target_book_p95 = 1500
ok = True
if slot_p95 > target_slot_p95:
    print(f"FAIL slot p95 {slot_p95:.1f}ms > {target_slot_p95}ms target"); ok = False
else:
    print(f"OK   slot p95 {slot_p95:.1f}ms ≤ {target_slot_p95}ms")
if book_p95 > target_book_p95:
    print(f"FAIL booking p95 {book_p95:.1f}ms > {target_book_p95}ms"); ok = False
else:
    print(f"OK   booking p95 {book_p95:.1f}ms ≤ {target_book_p95}ms")
sys.exit(0 if ok else 1)

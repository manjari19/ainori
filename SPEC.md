# Ainori: Real-Time Carpool Matching for SFU

Ainori (相乗り, Japanese for "sharing a ride") matches SFU students driving up Burnaby Mountain with riders near their route, in real time. Drivers post a trip, riders request a seat, and the system finds the best driver within seconds based on pickup distance, detour, seats left, and whether you'll make it to class on time.

It's built as a set of small Go services in the style of Uber's own architecture: H3 hexagon indexing, an event stream between services, gRPC for internal calls, and distributed tracing across the whole request path.

---

## 1. Goals

1. **Fully functional end to end:** a rider request goes in, a match comes out, and the live map shows it happen.
2. **Live on the internet for free:** one free Oracle Cloud VM for the backend, Netlify for the map.
3. **Real numbers for the resume:** match rate and p50/p95/p99 match latency come from actual simulator runs, never made up.
4. **Defensible:** Manjari can explain every component in plain English and answer follow-up questions. Section 13 covers this.

## 2. Non-goals (stretch, after the application)

Kubernetes, Cadence/Temporal workflows, surge pricing, real user accounts/auth, payments, a mobile app, real road-network ETAs.

---

## 3. Architecture

```
                 ┌──────────────┐   WebSocket (live updates)
  Browser map ◄──┤              │◄──────────────────────────┐
  (React +       │   gateway    │                           │
   deck.gl)  ───►│  REST + WS   │                           │
                 └──┬────────┬──┘                           │
          gRPC      │        │ XADD ride_requests           │ XREAD matches,
   UpdateLocation   │        ▼                              │ locations
                 ┌──▼──────────┐   ┌───────────────────┐    │
                 │  location   │   │  Redis Streams    │────┘
                 │  service    │──►│  (event bus)      │
                 └──────┬──────┘   └─────────┬─────────┘
                        │ H3 cell sets       │ consumer group
                        ▼                    ▼
                 ┌─────────────┐   ┌───────────────────┐
                 │   Redis     │◄──┤  matching service │──► Postgres (trips,
                 │ (live state)│   │  filter, score,   │     drivers, riders,
                 └─────────────┘   │  claim seat (Lua) │     recurring rides)
                        ▲          └─────────┬─────────┘
                        │  gRPC NearbyDrivers│
                        └────────────────────┘

  sim CLI ──► gateway (simulates hundreds of drivers and riders, prints a latency report)
  All services ──► OpenTelemetry ──► Jaeger (traces) and Prometheus ──► Grafana (metrics)
```

### Why each piece exists (plain English)

| Piece | What it does | Analogy |
|---|---|---|
| **H3** (Uber's hexagon grid) | Turns a lat/lng into a hexagon ID. "Drivers near me" becomes "drivers in my hexagon and the ring of hexagons around it." | Splitting a city map into honeycomb tiles so you only search nearby tiles, not the whole city. |
| **Redis (live state)** | Holds where every driver is right now and how many seats are left. Super fast, in memory. | A whiteboard everyone can read and write instantly. |
| **Redis Streams (event bus)** | Services drop events ("rider wants a ride") into a stream, and other services pick them up when ready. Nothing is lost if a service restarts. This is the job Kafka does at Uber. | A restaurant ticket rail: the waiter clips the order up, the kitchen grabs it when free. |
| **gRPC** | Fast, typed calls between services, defined in `.proto` files. | A strict phone line where both sides agreed on the script in advance. |
| **Postgres** | Permanent records: trips, users, recurring rides. | The filing cabinet. |
| **Lua seat claim** | Checks "seats > 0" and takes a seat in one atomic step, so two riders can never grab the last seat. | One person holds the only pen, so two people can't sign up for the same slot. |
| **OpenTelemetry + Jaeger** | Follows one request across every service and shows where the time went. Jaeger was built at Uber. | A parcel tracking page for a single request. |
| **Prometheus + Grafana** | Counters and graphs: matches per second, latency, why riders went unmatched. | The car dashboard. |

### Why Redis Streams instead of Kafka
Kafka is a distributed event log built for huge scale (partitions, long retention, many machines). Ainori runs on one free VM, so Redis Streams gives the same pattern (append-only stream, consumer groups, acknowledgements, replay of unacknowledged messages) with zero extra infrastructure. All publishing and consuming goes through a small `bus` interface, so swapping in Kafka later means writing one new adapter file. **Interview line:** "I used Redis Streams with consumer groups behind an interface. At Uber's scale I'd swap the adapter for Kafka to get partitioning and long-term retention."

---

## 4. Tech stack

| Layer | Choice |
|---|---|
| Backend language | Go 1.23+ (single Go module, monorepo) |
| Geospatial | `github.com/uber/h3-go/v4` (needs cgo) |
| Internal RPC | gRPC + Protocol Buffers (`buf` for codegen) |
| HTTP / WebSocket | `chi` router, `github.com/coder/websocket` |
| Live state + event bus | Redis 7 (`github.com/redis/go-redis/v9`), Redis Streams |
| Database | Postgres 16 (`github.com/jackc/pgx/v5`), SQL migrations (`golang-migrate`) |
| Observability | OpenTelemetry Go SDK, OTLP exporter, Jaeger all-in-one, Prometheus, Grafana |
| Frontend | React + TypeScript (Vite), deck.gl (`H3HexagonLayer`, `TripsLayer`, `ArcLayer`), `react-map-gl` + MapLibre, OpenFreeMap tiles (free, no API key) |
| Testing | Go `testing` with `-race`, integration tests against real Redis/Postgres, Vitest for frontend utils |
| CI | GitHub Actions (Go vet + test with Redis/Postgres service containers, frontend build) |
| Local run | Docker Compose, one command |
| Hosting | Oracle Cloud Always Free Ampere A1 VM (2 OCPU, 12 GB as of June 2026) running Docker Compose, Caddy for automatic HTTPS via an `sslip.io` hostname, Netlify for the frontend |

---

## 5. Repo layout

```
ainori/
├── CLAUDE.md
├── SPEC.md
├── NOTES.md                  # Manjari's interview cheat sheet, filled in per milestone
├── README.md
├── docker-compose.yml
├── deploy/
│   ├── docker-compose.prod.yml
│   ├── Caddyfile
│   ├── prometheus.yml
│   └── grafana/              # provisioned datasource + dashboard JSON
├── proto/ainori/v1/
│   ├── location.proto
│   └── matching.proto
├── go.mod
├── cmd/
│   ├── gateway/main.go
│   ├── location/main.go
│   ├── matching/main.go
│   └── sim/main.go
├── internal/
│   ├── bus/                  # Publisher/Consumer interfaces + Redis Streams adapter
│   ├── geo/                  # H3 helpers, haversine, detour estimate
│   ├── location/             # location service logic + Redis store
│   ├── matching/             # filters, scoring, seat claim, recurring rides
│   ├── cost/                 # gas cost split
│   ├── store/                # Postgres repositories
│   ├── gateway/              # REST handlers, WebSocket hub, demo mode
│   ├── sim/                  # simulator engine + latency report
│   └── telemetry/            # OTel + Prometheus setup, trace propagation through streams
├── migrations/
├── data/routes/              # precomputed GeoJSON routes to SFU
└── web/                      # Vite + React + TypeScript + deck.gl
```

---

## 6. Domain model

**Campus:** SFU Burnaby, `49.2781, -122.9199`. Every trip ends here (v1 is morning trips to campus only).

**Driver:** `id, name, gender (woman | man | nonbinary), seats_total, seats_left, route_id, current lat/lng, departure_time, fuel_l_per_100km (default 8.0), women_nb_only (bool)`

**Rider:** `id, name, gender, pickup lat/lng, class_start (time), women_nb_only (bool)`

**RideRequest:** `id, rider_id, pickup lat/lng, class_start, created_at`

**Trip (Postgres):** `id, driver_id, rider_ids[], matched_at, pickup_points, est_arrival, distance_km, cost_per_person`

**RecurringRide (Postgres):** `id, driver_id, rider_id, days_of_week (e.g. MON,WED,FRI), class_start, active`

### Redis keys
- `cell:{h3}` → SET of driver IDs currently in that res-8 hexagon
- `driver:{id}` → HASH of live driver state (lat, lng, cell, seats_left, gender, women_nb_only, eta_campus, last_seen)
- `drivers:last_seen` → ZSET (score = unix time) to expire drivers who stop pinging for 30s
- Streams: `stream:locations`, `stream:ride_requests`, `stream:matches`, each capped with `MAXLEN ~ 10000`

---

## 7. Matching logic (the part interviewers will poke at)

### Step 1: Candidate search with H3
1. Convert the rider's pickup to an H3 cell at **resolution 8** (hexagons about 0.5 km per edge).
2. Search `gridDisk(cell, k)` for k = 1, 2, 3 (expanding rings, roughly 1 to 3 km). Stop expanding once at least 5 candidates are found.
3. Candidates come back from the location service over gRPC (`NearbyDrivers`).

### Step 2: Hard filters (any fail means the driver is skipped, and the reason is counted in metrics)
- `seats_left > 0`
- **Women/NB-only:** if the rider has it on, the driver must be a woman or non-binary. If the driver has it on, the rider must be too. Both sides are respected.
- **Arrive before class:** estimated arrival at campus, including the pickup detour, must be at least **10 minutes before** `class_start`.

### Step 3: Score the rest (lower is better)
```
detour_min   = (time(driver→rider) + time(rider→campus)) - time(driver→campus)
pickup_km    = haversine(driver, rider)
slack_min    = class_start - est_arrival            (minutes)
early_penalty = max(0, slack_min - 30)              (arriving 2 hours early is bad too)

score = 1.0 * detour_min + 0.5 * pickup_km + 0.2 * early_penalty
```
Travel time uses `haversine_km * 1.3 (road factor) / 40 km/h`. That's a deliberate, documented simplification. Interview answer for "how would you improve it?": real road-network routing (OSRM), historical traffic by hour, and learned ETAs like Uber's DeepETA.

### Step 4: Claim the seat atomically
Walk candidates from best score to worst. For each one, run a **Lua script** in Redis that checks `seats_left > 0`, decrements it, and returns success, all in one atomic step. The first success wins. If every claim fails because someone else got there first, re-search once, then publish `unmatched` with reason `no_seats`.

### Step 5: Persist and publish
Write or update the Trip in Postgres, then `XADD stream:matches` with the match and the trace context. Then `XACK` the original request. If the service crashes before acknowledging, the request stays pending and gets reclaimed with `XAUTOCLAIM`, so no request is lost. That's the "at-least-once delivery" talking point. Because of it, the handler must be **idempotent**: it checks whether that request ID already has a trip before matching again.

### Gas cost split (`internal/cost`)
```
litres     = trip_distance_km * fuel_l_per_100km / 100
trip_cost  = litres * gas_price_per_litre        (configurable, env GAS_PRICE_PER_L)
per_person = trip_cost / (riders_on_trip + 1)     (driver pays a share too)
```
Rounded to cents. Shown on the map popup and in the match event.

### Recurring rides
- After a rider takes the same driver twice, the API offers a "make this recurring" option: `POST /recurring` with days of the week.
- At startup and then every minute, the matching service checks for recurring rides due in the next 60 minutes and **pre-reserves** the seat with the same Lua claim.
- If the regular driver isn't online or is full, it falls back to normal matching and flags `recurring_fallback` in the match event.

---

## 8. APIs

### gRPC (`proto/ainori/v1`)
- `LocationService.UpdateLocation(DriverPing) → Ack`
- `LocationService.NearbyDrivers(NearbyRequest{cell, max_k, min_results}) → DriverList`
- `MatchingService.GetTrip(TripID) → Trip` (used by the gateway for lookups)

### Gateway REST
- `POST /drivers`, `POST /drivers/{id}/ping`
- `POST /riders`, `POST /ride-requests` → returns `request_id` right away (async), and the result arrives over WebSocket
- `GET /trips/{id}`
- `POST /recurring`, `GET /riders/{id}/recurring`
- `POST /demo/start` → runs a 2-minute light simulation so live visitors see action. One at a time, rate-limited per IP.
- `GET /healthz`, `GET /metrics`

### WebSocket `/ws`
Pushes `driver_moved`, `request_created`, `matched`, `unmatched{reason}`, `demand_hex{cell, count}` events (batched every 250ms so the browser isn't flooded).

---

## 9. Simulator CLI (`cmd/sim`): the "tools/infrastructure" piece

```
go run ./cmd/sim --drivers 200 --riders 600 --duration 2m --rate 10 --target http://localhost:8080
```
- Drivers follow precomputed real routes from Coquitlam Centre, Lougheed, Metrotown, Surrey Central, Commercial-Broadway, Port Moody, and New Westminster to SFU (`data/routes/*.geojson`), pinging every 2 seconds.
- Riders appear along those corridors with random class start times (8:30, 9:30, 10:30, 11:30), and about 30% have women/NB-only on.
- Measures request→match latency by listening on the WebSocket.
- Prints and saves a report (`reports/run-<timestamp>.json`):

```
Requests: 600   Matched: 571 (95.2%)   Unmatched: 29
  no_seats 14 | late_for_class 9 | no_driver_nearby 4 | preference 2
Match latency  p50 18ms  p95 41ms  p99 77ms
Throughput     10.0 req/s sustained
```
Run it at increasing `--rate` to find where p99 climbs. That becomes a README chart and a resume number.

---

## 10. Live map (`web/`)

- Basemap: MapLibre + OpenFreeMap, centred on Burnaby/Coquitlam with SFU marked.
- `H3HexagonLayer`: demand heat (open requests per hex), colour scale from cool to warm.
- `TripsLayer` or `IconLayer`: drivers moving along their routes.
- `ArcLayer`: a line drawn from rider to driver the moment a match lands, fading out after 3s.
- Side panel: live counters (online drivers, open requests, match rate, p95 latency), the unmatched-reasons breakdown, a "Run demo" button, and a click-a-match popup showing detour minutes, arrival time vs class start, and gas cost per person.
- Must work at phone width. No overlapping text anywhere.

---

## 11. Observability

- Every service starts an OTel tracer. gRPC uses `otelgrpc` interceptors.
- **Trace context goes through Redis Streams:** the publisher writes the `traceparent` header into the stream message fields, and the consumer reads it and continues the same trace. So one Jaeger trace shows gateway → stream → matching → location gRPC → Redis Lua → Postgres → stream → gateway WebSocket. This is a strong interview talking point.
- Prometheus metrics: `ainori_match_latency_seconds` (histogram), `ainori_matches_total`, `ainori_unmatched_total{reason}`, `ainori_drivers_online`, `ainori_stream_pending{stream}`.
- Grafana dashboard provisioned from JSON in `deploy/grafana/`.

---

## 12. Testing

| Test | What it proves |
|---|---|
| Scoring and filter unit tests (table-driven) | Late drivers, wrong preference, and full cars are filtered. Lower detour wins. |
| H3 ring search test | Expands k until it hits min results, never past max_k. |
| **Seat claim concurrency test** | 50 goroutines race for a 3-seat car against real Redis, and exactly 3 succeed. Run with `-race`. |
| Idempotency test | Delivering the same request twice creates one trip. |
| Crash-recovery test | An unacknowledged request gets reclaimed and matched. |
| Cost split tests | Rounding, single rider, full car. |
| Recurring ride test | Pre-reserves the regular driver, and falls back when that driver is offline. |
| End-to-end smoke test | Compose up, sim for 20s, match rate > 80%. |

All real. No mocked numbers in the README or resume.

---

## 13. Milestones (one day, about 11 hours)

Each milestone ends with: tests passing, a short plain-English update in `NOTES.md`, an **explain-it-back quiz** (Claude asks 3 questions, Manjari answers in her own words, Claude corrects), and a suggested commit message. **Manjari makes every commit herself.**

| # | Milestone | Time | Done when |
|---|---|---|---|
| M0 | Repo, Go module, Docker Compose (Redis, Postgres, Jaeger), proto files + codegen, migrations | 0:45 | `docker compose up` is healthy, `buf generate` works |
| M1 | Location service: pings → H3 cell → Redis sets + hash, stale driver cleanup, `NearbyDrivers` gRPC, publish to `stream:locations` | 1:30 | Unit + ring-search tests pass |
| M2 | Matching service: consumer group, filters, scoring, Lua seat claim, Postgres trip, cost split, idempotency, `XAUTOCLAIM` recovery | 2:00 | Concurrency, idempotency, and recovery tests pass |
| M3 | Gateway: REST, WebSocket hub with batching, demo mode | 1:00 | curl a request and see the match over WebSocket |
| M4 | Simulator CLI + route data + JSON report | 1:00 | First real report with real numbers |
| M5 | Live map (deck.gl) | 2:00 | Demo shows hexes, moving cars, and match arcs live |
| M6 | Tracing through streams, Prometheus metrics, Grafana dashboard | 1:00 | One Jaeger trace spans every service |
| M7 | Recurring rides | 0:45 | Recurring tests pass |
| M8 | CI, deploy to Oracle VM + Netlify, README with architecture diagram, demo GIF, load-test chart | 1:30 | Live link works over HTTPS |

**If the day runs long, cut in this order:** Grafana dashboard (keep `/metrics`), then recurring rides (move to stretch), then live deploy (fall back to a demo GIF). Never cut tests or the explain-it-back quizzes.

---

## 14. Deployment

1. Create an Oracle Cloud Always Free account (needs a card for verification, but there's no charge on Always Free shapes). Create an Ampere A1 VM (2 OCPU, 12 GB, Ubuntu). Open ports 80/443 in the security list **and** in the VM's iptables (a common Oracle gotcha).
2. Install Docker, clone the repo, `docker compose -f deploy/docker-compose.prod.yml up -d --build` (building on the ARM VM itself avoids cgo cross-compile pain with h3-go).
3. Caddy serves `https://<ip-with-dashes>.sslip.io` with an automatic Let's Encrypt cert and reverse-proxies `/api` and `/ws` to the gateway. Jaeger and Grafana stay on private ports, reachable through an SSH tunnel only.
4. Frontend on Netlify, with `VITE_API_URL` pointing at the sslip.io URL. CORS only allows the Netlify domain.
5. Postgres and Redis are not exposed publicly. Their data lives in Docker volumes.

**Fallback if Oracle signup or capacity fails:** run locally and record the demo GIF. The resume says "GitHub" instead of "Live."

---

## 15. Resume entry (fill in only with real numbers from your own runs)

**Ainori: Real-Time Carpool Matching for SFU** | Go, gRPC, Redis Streams, H3, PostgreSQL, OpenTelemetry, React, deck.gl
- Built a real-time carpool matching system in **Go** for SFU commuters, using Uber's **H3** hexagon index to find nearby drivers and filter by class start time, seats, and a women/non-binary-only safety option
- Designed **4 services** that communicate over **gRPC** and **Redis Streams** with at-least-once delivery, idempotent handlers, and an atomic **Lua** seat claim that prevents double-booking (verified with a **50-goroutine race test**)
- Built a load-testing CLI that simulates **[N] drivers and [M] riders** on real routes, sustaining **[X] req/s** at **[p99] ms p99** match latency, with **OpenTelemetry** traces spanning every service
- Streamed matches to a live **deck.gl** map over WebSocket, showing demand hexes, moving cars, and per-rider gas cost splits

Replace every bracket with a number from a saved `reports/` file. If a number doesn't exist, the bullet changes. It never gets estimated.

# Resume bullets

Only features that are built, run, and tested. Numbers come from `reports/run-20261029-094801-rate10.json` and `reports/run-20261029-094910-rate25.json` (see `RESULTS.md`).

**Ainori: Real-Time Carpool Matching for SFU** | Go, gRPC, Redis Streams, H3, PostgreSQL, OpenTelemetry, React, deck.gl

- Built a real-time carpool matching system in **Go** for SFU commuters, using Uber's **H3** hexagon index to find nearby drivers and filter by seats, class start time, and a women/non-binary-only safety option
- Split it into **3 services** (gateway, location, matching) that communicate over **gRPC** and **Redis Streams** with at-least-once delivery, idempotent handlers, crash recovery through `XAUTOCLAIM`, and an atomic **Lua** seat claim that prevents double-booking (verified with a **50-goroutine race test**)
- Wrote a load-testing CLI that simulates **200 drivers** on 7 real driving routes and **600 riders**, measuring **p99 match latency of 31.5 ms at 25 req/s** (p99 33.7 ms at 10 req/s) end to end, with **OpenTelemetry** traces spanning the gateway, both streams, matching, the location service, Redis, and Postgres
- Streamed matches to a live **deck.gl** map over WebSocket, showing demand hexagons, driver positions, match arcs, and per-rider gas cost splits

Not included on purpose because they are not built or not verified: recurring rides, Grafana dashboard, live deployment, GitHub Actions run.

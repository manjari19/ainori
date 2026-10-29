# Results

Every number here comes from a saved report in `reports/`. Nothing is estimated.

## Runs

Same seed (20261029), 200 simulated drivers, 600 ride requests, drivers moving at 10x real time, run on one machine (Apple M5, 10 cores, macOS 26.5.1). The three Go services, Redis, Postgres, Jaeger and the simulator all ran on that one laptop, with tracing on.

| | Rate 10 req/s | Rate 25 req/s |
|---|---|---|
| Report file | `reports/run-20261029-094801-rate10.json` | `reports/run-20261029-094910-rate25.json` |
| Requests sent | 600 | 600 |
| Matched | 498 (83.0%) | 474 (79.0%) |
| Unmatched | 102 | 126 |
| Unmatched reasons | no_seats 75, no_driver_nearby 24, preference 3 | no_seats 115, no_driver_nearby 7, preference 4 |
| Match latency p50 | 6.2 ms | 5.6 ms |
| Match latency p95 | 15.0 ms | 7.9 ms |
| Match latency p99 | 33.7 ms | 31.5 ms |
| Match latency max | 36.6 ms | 37.9 ms |
| Sustained rate | 10.0 req/s over 60 s | 25.0 req/s over 24 s |
| Request errors / ping errors / no response | 0 / 0 / 0 | 0 / 0 / 0 |

## How latency is measured

The simulator starts a stopwatch just before it sends `POST /ride-requests` and stops it when the `matched` event for that request arrives over its WebSocket. So it covers the HTTP call, the gateway, the request stream, the matcher, the gRPC call to the location service, the Redis seat claim, the Postgres write, the match stream and the WebSocket push. It uses only the simulator's own clock. Match latency counts matched requests only.

## What these numbers do and do not show

- At 25 req/s the system kept the same latency as at 10 req/s. I did not push the rate higher, so **I have not found where p99 starts to climb**. The only claim supported is "no degradation up to 25 req/s on this machine".
- Each rate was run once. There is no variance estimate. The 10 req/s p95 (15.0 ms) versus 25 req/s p95 (7.9 ms) gap is probably warm-up and noise, not a real speed-up.
- Everything was on localhost, so there is no network latency between the parts. A deployed system would be slower.
- The match rate depends on the simulation setup, not only the system. Riders and drivers are placed along 7 real routes, 30% of riders have the women/non-binary-only setting on, and class start times are 35, 65, 125 or 185 minutes away. Most unmatched riders hit `no_seats`, because many riders appear near the same stretch of road at once.
- Routes are real driving routes from the public OSRM server, but travel time in the matcher is the documented simplification `haversine * 1.3 / 40 km/h`.

## Tests

Run with `go test -race -count=1 ./...` against real Redis and Postgres:
- Seat claim concurrency: 50 goroutines race for a 3-seat car, exactly 3 succeed.
- Last seat race through the whole service: 5 riders, 1 seat, 1 match, 4 `no_seats`.
- Idempotency: the same request delivered twice makes one trip and takes one seat.
- Crash recovery: a message read but never acknowledged is reclaimed and matched.
- Filters, scoring, cost split, H3 ring search, stale driver cleanup, gateway REST and WebSocket end to end, demo rate limiting, trace context through stream fields.
- Frontend: 7 Vitest tests on the event reducer.

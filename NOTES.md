# Ainori interview notes

## M0: Repo, Docker Compose, proto contracts, migrations

### What it does
M0 sets up the building site. One command (`docker compose up -d --wait redis postgres jaeger`) starts Redis, Postgres and Jaeger. A one-shot `migrate` container builds the database tables. The `.proto` files describe what the services will say to each other, and `buf generate` turns them into typed Go code.

Analogy: before opening a restaurant you wire the kitchen, buy the fridges and agree on the order-slip format. No food is cooked yet, but nothing later works without it.

### Key design decisions
- **Contracts first (protobuf + buf).** The location and matching services can be built and tested separately because the message shapes are fixed up front. Tradeoff: a small amount of codegen tooling (two plugins plus buf) in exchange for typed calls and no hand-written JSON parsing.
- **`NearbyDrivers` returns full driver state, not just IDs.** The matcher gets everything it needs in one round trip. Tradeoff: bigger responses, but far fewer Redis calls, which matters for p99 latency.
- **`ride_requests` table with `trip_id`.** The request ID is the idempotency key. If the same stream message is delivered twice, the second delivery finds the trip already attached and does nothing. Tradeoff: one extra table compared with only having `trips`.
- **Migrations run as a separate one-shot container**, not inside the Go services. Services never race to migrate on startup, and the schema is versioned in plain SQL files.
- **Postgres is on host port 5433**, because 5432 was already taken by an SSH tunnel on this machine. Inside Docker it is still 5432.
- **Generated code is committed (`gen/`).** CI and teammates don't need buf installed to build. Tradeoff: generated files show up in diffs.

### Likely interview questions
1. **Why define gRPC contracts in .proto files instead of using JSON over HTTP?**
   The proto file is a single source of truth that generates client and server code in a typed, compact binary format. Both sides agree on the schema at compile time, so mismatches are caught before runtime. JSON would work but is bigger, slower to parse, and has no compile-time check.
2. **How do you make sure a database schema change is applied safely and repeatably?**
   Versioned SQL migrations (golang-migrate) with an up and a down file. The tool records the current version in `schema_migrations`, so running it twice is safe and every environment ends up with the same schema.
3. **Why is the request ID stored in Postgres?**
   Redis Streams gives at-least-once delivery, so a message can arrive twice (for example after a crash before the ack). The request ID with its `trip_id` lets the handler see "already done" and skip, so a rider never gets two trips.

## M1: Location service

### What it does
Drivers ping their position. The service turns it into an H3 hexagon, files the driver under that hexagon in Redis, and announces the ping on `stream:locations`. Asking "who is near this rider?" looks in the rider's hexagon and then widens ring by ring (k=1, 2, 3) until enough drivers turn up. Drivers who go quiet for 30 seconds are swept out.

Analogy: a honeycomb wall of pigeonholes. Each driver's name card sits in the pigeonhole for where they are. To find nearby drivers you check your own hole and the holes around it, not the whole wall.

### Key design decisions
- **Ping does not overwrite `seats_left` after the first ping (HSETNX).** The matcher changes seats through the atomic Lua claim. If pings could write seats, a late ping would hand back a seat that was already given away. Tradeoff: a driver cannot change their seat count by pinging; that would need a separate call.
- **`NearbyDrivers` returns full driver state in one pipelined read.** Fewer round trips means lower p99. Tradeoff: slightly bigger responses.
- **Ring search stops early** once `min_results` is reached, so crowded areas stay cheap. Tradeoff: it may skip a better driver one ring further out. Scoring only sees what the search returned.
- **Stale cleanup by a ZSET of last-seen times**, swept every 5 seconds. Tradeoff: a driver can look online for up to 35 seconds after disappearing.
- **Bus behind an interface.** Location only knows `Publisher`, so Kafka could replace Redis Streams in one file.

### Likely interview questions
1. **Why H3 instead of just computing the distance to every driver?**
   Distance to every driver is O(all drivers) per request. With H3 the lookup is O(drivers in a handful of cells), because a cell ID is a key you can index. Uber built H3 for exactly this: a uniform hexagon grid where all neighbours are the same distance away.
2. **What happens if a driver stops pinging?**
   A ZSET keeps their last-seen time. A background reaper removes anyone older than 30 seconds from the cell set, the driver hash and the ZSET, so the matcher never offers a ghost driver.
3. **Why a hexagon rather than a square grid?**
   All six neighbours of a hexagon are the same distance from its centre. Squares have corner neighbours that are further away, which makes "within k rings" a lumpier shape.

## M2: Matching service

### What it does
The matcher takes a ride request off the `stream:ride_requests` stream, asks the location service for nearby drivers, throws out the ones who can't take the rider (full, wrong women/NB-only setting, would arrive less than 10 minutes before class), and scores the rest. Then it tries to take a seat from the best one with a Lua script, saves the trip in Postgres, and announces the result on `stream:matches`. Only after that does it acknowledge the request.

Analogy: a kitchen chef takes the next ticket off the rail, checks which delivery drivers can do it, picks the best one, and signs the one sheet that says "seat taken" using the single shared pen. The ticket only comes off the rail once the order is written in the filing cabinet.

### Key design decisions
- **Lua seat claim.** "Check seats > 0, then decrement" is two steps; two workers could both see 1 seat. Redis runs a Lua script as one uninterrupted step, so exactly one wins. Tradeoff: logic lives in Redis, so it is harder to unit test without a real Redis (the tests use one).
- **Ack only after the outcome is saved (at-least-once).** A crash leaves the message pending and `XAUTOCLAIM` hands it to another worker. Tradeoff: duplicates are possible, so the handler has to be idempotent.
- **Idempotency through the stored outcome.** The `ride_requests` row holds the status and the published event. A redelivered message finds it, re-publishes the same event, and does not match again. A conditional `UPDATE ... WHERE status='open'` also stops two workers deciding the same request at once.
- **Seat released if the save fails.** The claim and the Postgres write are two different systems, so there is no shared transaction. If the save fails after the claim, a Lua release gives the seat back and the message is retried. Tradeoff: a crash exactly between the claim and the release can leak a seat until the driver re-registers. A real fix would be a reservation with a TTL.
- **Seat count and cost per match.** A trip row is created per matched rider. Cost per person uses how many seats are already taken (`seats_total - seats_left`) plus the driver.
- **Travel time is `haversine * 1.3 / 40 km/h`.** Documented simplification. A real system would use road routing and traffic.

### Likely interview questions
1. **Two riders request the last seat at the same moment. What happens?**
   Both workers rank the same driver first and run the Lua claim. Redis executes the scripts one after the other, so the first sees seats=1 and takes it, the second sees 0 and fails. The loser tries the next driver or re-searches once, then reports `no_seats`. The 50-goroutine test and the 5-riders-one-seat test prove it.
2. **What does at-least-once delivery mean, and how do you cope with it?**
   A message is delivered one or more times, never zero. The consumer acknowledges only after saving the result, so a crash causes a redelivery. That means the handler must be idempotent: here, it checks the stored outcome for the request ID and re-publishes it rather than matching again.
3. **Why Redis Streams instead of Kafka?**
   Same pattern (append-only log, consumer groups, acks, reclaim) with zero extra infrastructure on one VM. The bus is behind an interface, so a Kafka adapter is one new file. Kafka would add partitioning and long retention at much larger scale.

## M3: Gateway

### What it does
The gateway is the front desk. Browsers and the simulator talk to it with plain HTTP (`POST /drivers`, `/riders`, `/ride-requests`, driver pings) and it answers immediately. A ride request is saved, put on the `stream:ride_requests` stream, and the gateway replies with a `request_id`. The match shows up later over a WebSocket (`/ws`). A `POST /demo/start` button runs a small built-in simulation for visitors.

Analogy: a hotel front desk. You hand over your request and get a ticket number straight away. When the room is ready, the desk calls you, instead of making you stand there waiting.

### Key design decisions
- **Async request/response.** `POST /ride-requests` returns 202 with an ID and the result arrives over WebSocket. Tradeoff: the client has to correlate by ID, but the HTTP handler never waits on matching, so a slow match cannot pile up open connections.
- **Matches go out immediately, driver moves and demand counts are batched every 250ms.** Batching keeps the browser from being flooded by hundreds of pings per second. Sending matches immediately keeps the measured latency honest. Tradeoff: two code paths.
- **Slow WebSocket clients are dropped**, not waited for. One slow browser cannot stall everyone. Tradeoff: that client has to reconnect.
- **Small in-memory caches for drivers and riders** so a ping or a request does not need a Postgres read each time. Tradeoff: a second gateway instance would warm its own cache; fine because rows never change after creation.
- **Each gateway tails the streams itself (`XREAD`, no consumer group)** because every gateway needs every event. A consumer group would split events between instances.
- **Demo mode is one-at-a-time and rate-limited per IP**, with a time cap, so a public button cannot be used to flood the server.

### Likely interview questions
1. **Why return 202 and push the result over a WebSocket instead of waiting in the HTTP call?**
   Matching involves several services and can take a variable time. Holding the connection ties up resources on the slowest path. Returning a ticket and pushing the result decouples the two and keeps the front door fast.
2. **How do you stop one slow browser from affecting others?**
   Each client has a bounded buffer. If it fills, that client is disconnected, so the broadcast loop never blocks.
3. **Why batch some events and not others?**
   Driver positions arrive constantly and the map only needs the latest one every quarter second, so batching cuts traffic. Matches are rare and latency-sensitive, so they go straight out.

## M4: Simulator and routes

### What it does
`cmd/routegen` fetched 7 real driving routes to SFU from the public OSRM server once and saved them as GeoJSON in `data/routes` (committed). `cmd/sim` plays hundreds of drivers along those routes, pinging every 2 seconds, and riders appearing near the routes at a fixed rate. It listens on the WebSocket, measures request-to-outcome time, and saves a JSON report in `reports/`.

Analogy: a crash-test dummy fleet. It drives the real road network of your system on purpose and records how it handled the traffic.

### Key design decisions
- **Open-loop load.** Requests go out on a schedule even if earlier ones are slow. A closed loop (send the next after the last finishes) would slow down along with the server and hide overload. Tradeoff: if the server falls behind, results look worse, which is the honest outcome.
- **Latency measured from the client's clock only**: from just before the POST to the moment the matched event arrives over the WebSocket. It includes HTTP, both streams, the matcher, gRPC, Redis, and Postgres. No server-side timing is mixed in.
- **Drivers move 10x faster than real time by default** (flag `--speed`), otherwise a 2-minute run would barely move anyone. This is stated in each report.
- **Class start times are minutes from now** (35, 65, 125, 185) rather than clock times, so a run gives sensible results at any hour of the day. The 35-minute slot makes some riders genuinely too late.
- **Fixed seed flag** makes a run repeatable.
- **Routes are fetched once and committed.** The simulator has no network dependency at run time.

### Likely interview questions
1. **What is the difference between open-loop and closed-loop load testing, and which did you use?**
   Closed-loop clients wait for a response before sending the next request, so a slow server reduces the load and hides the problem (coordinated omission). Open-loop sends on a timer regardless. I used open-loop.
2. **How do you know your latency numbers are real?**
   They are computed from timestamps on the simulator's own clock and saved with the run parameters in `reports/`. The README only quotes those files.
3. **What would limit throughput first?**
   Whatever saturates first. The run at increasing rates shows where p99 climbs. Candidates are Postgres round trips per request, the Redis stream consumers, and gRPC calls to the location service.

## M5: Live map (basic)

### What it does
A React page with a MapLibre basemap (OpenFreeMap tiles, no API key) and deck.gl layers on top. Open requests show as coloured hexagons (cool to warm as more riders wait in the same hexagon), drivers show as blue dots, SFU is a red dot, and each new match draws an arc from rider to driver that fades after 3 seconds. A side panel shows live counters, why riders went unmatched, a "Run demo" button, and a tappable list of recent matches with details (detour, arrival vs class start, gas cost each). On a phone the map sits above the panel instead of beside it.

Analogy: a departures board for carpools. The browser just listens and draws what the server says is happening.

### Key design decisions
- **All incoming events go through one pure reducer** (`src/lib/events.ts`). It has no React in it, so it is unit tested with Vitest (7 tests). Tradeoff: a bit more structure than putting state in components.
- **The browser does no matching or counting of its own.** The gateway sends totals and demand per hexagon, so the page stays thin and cannot disagree with the server.
- **Arcs fade from a 100ms clock** and stale drivers (no ping for 30s) are removed on the same clock.
- **Match details live in the side panel, not a map popup**, because arcs vanish in 3 seconds and popups are fiddly on a phone.
- **No polish by choice** (time): dots instead of car icons, no moving trails.

### Likely interview questions
1. **Why a reducer for the WebSocket state?**
   Every event is an input to a pure function `(state, event) -> state`. That makes the behaviour easy to test without a browser and avoids tangled setState calls.
2. **Why does H3 show up in the frontend too?**
   The server tells the browser which hexagon has how many open requests, and deck.gl's `H3HexagonLayer` draws a hexagon from just its ID. Sending an ID is much smaller than sending a polygon.
3. **How would it handle thousands of drivers?**
   Drivers are already batched every 250ms and deck.gl draws on the GPU. The first thing to change would be sending only drivers in the current viewport.

## M6: Tracing and metrics (Grafana skipped)

### What it does
Every service reports spans to Jaeger. The trick is that a request hops through Redis Streams, where there is no HTTP header to carry the trace ID. So the publisher writes a `traceparent` field into the stream message, and the consumer reads it and continues the same trace. In Jaeger, one ride request shows as a single trace of 9 spans: gateway, publish to the stream, matching, the gRPC call to location, the Redis Lua seat claim, the Postgres write, the publish of the match, and the gateway pushing it to the WebSocket. The matcher also exposes Prometheus metrics on `:9102/metrics`, and the gateway on `/metrics`.

Analogy: a parcel tracking page. Each handoff scans the same tracking number, so you can see where the time went.

### Key design decisions
- **Trace context lives inside the stream message.** Streams have no headers, so the W3C `traceparent` value is just another field. Tradeoff: consumers must know to look for it.
- **Injection happens inside the bus publisher**, so no service can forget to propagate the trace.
- **gRPC uses `otelgrpc` stats handlers**, which propagate the trace automatically through gRPC metadata.
- **If `OTLP_ENDPOINT` is empty, tracing is off** and spans are no-ops. Tests and local runs do not need Jaeger.
- **Test for the important property**: a span started after reading a message is a child of the publisher's span, in the same trace (`internal/telemetry`).
- **Metrics:** `ainori_match_latency_seconds`, `ainori_matches_total`, `ainori_unmatched_total{reason}`, `ainori_drivers_online`, `ainori_stream_pending{stream}`. A Grafana dashboard was skipped for time; `/metrics` is kept.

### Likely interview questions
1. **How does a trace survive going through a message queue?**
   The producer serialises the current span context (W3C `traceparent`) into the message. The consumer extracts it and starts its span as a child. Without that, the trace would break into two disconnected ones at every queue.
2. **What is the difference between metrics and traces?**
   Metrics are cheap aggregates over many requests (rates, histograms) and tell you something is wrong. A trace follows one request and tells you where the time went.
3. **Why does `ainori_stream_pending` matter?**
   It is the number of messages read but not acknowledged. If it grows, the matcher is stuck or crashing, which is exactly the situation at-least-once delivery is meant to recover from.

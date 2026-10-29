# Study quiz (answer out loud, then check NOTES.md)

## M0
1. Why does `ride_requests` store a `trip_id`? What goes wrong without it?
2. Why generate Go code from `.proto` files instead of hand-writing JSON handlers?
3. Why does the `migrate` container run separately from the services?

## M1
1. What is an H3 cell, and why search a cell plus rings around it?
2. Why does a ping not overwrite `seats_left`?
3. How does the service notice a driver who went offline?

## M2
1. Why is the seat claim a Lua script and not two Redis commands?
2. A worker crashes after claiming a seat but before acking. Walk through what happens next.
3. What stops the same request from creating two trips?
4. Which two systems can disagree after a claim, and how do we handle a failed save?

## M3
1. Why does `POST /ride-requests` return before the match is known?
2. Why are match events sent immediately but driver moves batched?
3. What happens to a WebSocket client that cannot keep up?

## M4
1. Open loop vs closed loop: what does each hide or show?
2. Where exactly does the simulator start and stop its stopwatch?
3. Why were routes fetched once and committed instead of fetched on every run?

## M5
1. Why is the event handling a pure reducer?
2. How does an H3 cell ID become a drawn hexagon in the browser?
3. What would you change first if the map had 10,000 drivers?

## M6
1. How does a trace continue across a Redis Stream hop?
2. What would the Jaeger trace look like if the publisher forgot to inject the context?
3. What does a growing `ainori_stream_pending` tell you?

## Replay mode
1. How does the map decide between live and replay?
2. Why is the replay fed through the same reducer as live events?
3. Why was open requests per hexagon always zero, and how was the heat display fixed?

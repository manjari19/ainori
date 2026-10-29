// Records every WebSocket event from a running gateway, with timestamps, into a compact
// JSON file the map can replay when no backend is reachable.
//
//   node scripts/record.mjs [seconds] [out]    (run the simulator while it records)
import { writeFileSync } from 'node:fs'

const seconds = Number(process.argv[2] ?? 135)
const out = process.argv[3] ?? 'public/demo/recording.json'
const url = (process.env.API_URL ?? 'http://localhost:8080').replace(/^http/, 'ws') + '/ws'

const ids = new Map()
const short = (prefix, id) => {
  const key = prefix + id
  if (!ids.has(key)) ids.set(key, prefix + ids.size.toString(36))
  return ids.get(key)
}
const r5 = (x) => Math.round(Number(x) * 1e5) / 1e5

const events = []
let t0 = null
let lastStats = -1000

const ws = new WebSocket(url)
ws.onopen = () => console.log('recording from', url, 'for', seconds, 's')
ws.onmessage = (e) => {
  const now = performance.now()
  const msg = JSON.parse(String(e.data))
  // Start the clock at the first real event so the replay has no dead air at the start.
  if (t0 === null) {
    if (msg.type === 'stats' || msg.type === 'demand_hex') return
    t0 = now
  }
  const t = Math.round(now - t0)
  switch (msg.type) {
    case 'driver_moved':
      events.push([t, { type: 'driver_moved', data: msg.data.map((d) => ({ id: short('d', d.id), lat: r5(d.lat), lng: r5(d.lng) })) }])
      break
    case 'demand_hex':
      events.push([t, msg])
      break
    case 'matched': {
      const m = msg.data
      events.push([t, { type: 'matched', data: {
        request_id: short('r', m.request_id), rider_id: short('p', m.rider_id), driver_id: short('d', m.driver_id),
        trip_id: short('t', m.trip_id), detour_min: m.detour_min, pickup_km: m.pickup_km,
        est_arrival_unix: m.est_arrival_unix, class_start_unix: m.class_start_unix,
        cost_per_person: m.cost_per_person, distance_km: m.distance_km,
        rider_lat: r5(m.rider_lat), rider_lng: r5(m.rider_lng), driver_lat: r5(m.driver_lat), driver_lng: r5(m.driver_lng),
      } }])
      break
    }
    case 'stats':
      if (t - lastStats >= 1000) {
        lastStats = t
        events.push([t, msg])
      }
      break
  }
}
ws.onerror = () => { console.error('could not connect to', url); process.exit(1) }

setTimeout(() => {
  const durationMs = events.length ? events[events.length - 1][0] + 1000 : 0
  writeFileSync(out, JSON.stringify({ version: 1, durationMs, events }))
  console.log('saved', events.length, 'events,', Math.round(durationMs / 1000), 's ->', out)
  process.exit(0)
}, seconds * 1000)

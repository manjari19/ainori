export interface Match {
  requestId: string
  riderId: string
  driverId: string
  tripId: string
  detourMin: number
  pickupKm: number
  estArrivalUnix: number
  classStartUnix: number
  costPerPerson: number
  distanceKm: number
  rider: [number, number] // lng, lat
  driver: [number, number]
  at: number // ms when it reached the browser
}

export interface Stats {
  p99_ms: number
  drivers_online: number
  open_requests: number
  matched: number
  unmatched: number
  match_rate: number
  unmatched_reasons: Record<string, number>
  p95_ms: number
}

export interface Driver {
  id: string
  pos: [number, number]
  prev: [number, number] // where it was at the previous ping, for smooth movement
  seen: number // ms when the latest ping arrived
}

export const PING_MS = 2000

// Position between the previous and latest ping, so dots glide instead of jumping every 2s.
export function driverPosition(d: Driver, now: number): [number, number] {
  const f = Math.max(0, Math.min(1, (now - d.seen) / PING_MS))
  return [d.prev[0] + (d.pos[0] - d.prev[0]) * f, d.prev[1] + (d.pos[1] - d.prev[1]) * f]
}

// Requests are matched in milliseconds, so "open requests per hexagon" is almost always
// empty. Each hexagon instead keeps a heat level that rises with every request there
// and cools over a few seconds, which shows where demand is coming from.
export interface Heat {
  level: number
  at: number
}
export const HEAT_COOL_MS = 2500
export const HEAT_MAX = 6

export function heatLevel(h: Heat, now: number): number {
  return Math.max(0, h.level - (now - h.at) / HEAT_COOL_MS)
}

export interface State {
  heat: Record<string, Heat>
  connected: boolean
  drivers: Record<string, Driver>
  demand: Record<string, number>
  arcs: Match[] // matches younger than ARC_MS
  recent: Match[] // newest first, capped
  stats: Stats | null
}

export const ARC_MS = 3000
export const DRIVER_TTL_MS = 30000
const RECENT_MAX = 30

export const initialState: State = {
  heat: {},
  connected: false,
  drivers: {},
  demand: {},
  arcs: [],
  recent: [],
  stats: null,
}

export interface Message {
  type: string
  data: unknown
}

export type Action =
  | { kind: 'connected'; value: boolean }
  | { kind: 'reset' }
  | { kind: 'message'; raw: string; now: number }
  | { kind: 'event'; msg: Message; now: number }
  | { kind: 'tick'; now: number }

interface RawMatch {
  request_id: string
  rider_id: string
  driver_id: string
  trip_id: string
  detour_min: number
  pickup_km: number
  est_arrival_unix: number
  class_start_unix: number
  cost_per_person: number
  distance_km: number
  rider_lat: number
  rider_lng: number
  driver_lat: number
  driver_lng: number
}

export function toMatch(r: RawMatch, now: number): Match {
  return {
    requestId: r.request_id,
    riderId: r.rider_id,
    driverId: r.driver_id,
    tripId: r.trip_id,
    detourMin: r.detour_min ?? 0,
    pickupKm: r.pickup_km ?? 0,
    estArrivalUnix: r.est_arrival_unix,
    classStartUnix: r.class_start_unix,
    costPerPerson: r.cost_per_person ?? 0,
    distanceKm: r.distance_km ?? 0,
    rider: [r.rider_lng, r.rider_lat],
    driver: [r.driver_lng, r.driver_lat],
    at: now,
  }
}

// Minutes between arriving and class starting, shown in the match details.
export function slackMinutes(m: Pick<Match, 'estArrivalUnix' | 'classStartUnix'>): number {
  return Math.round((m.classStartUnix - m.estArrivalUnix) / 60)
}

// 1 when new, 0 once the arc is ARC_MS old.
export function arcOpacity(ageMs: number): number {
  return Math.max(0, Math.min(1, 1 - ageMs / ARC_MS))
}

export function reducer(state: State, action: Action): State {
  switch (action.kind) {
    case 'connected':
      return { ...state, connected: action.value }
    case 'reset':
      return { ...initialState, connected: state.connected }
    case 'tick': {
      const arcs = state.arcs.filter((a) => action.now - a.at < ARC_MS)
      const heat: Record<string, Heat> = {}
      for (const [cell, h] of Object.entries(state.heat)) {
        if (heatLevel(h, action.now) > 0) heat[cell] = h
      }
      const drivers: Record<string, Driver> = {}
      let changed = arcs.length !== state.arcs.length || Object.keys(heat).length !== Object.keys(state.heat).length
      for (const [id, d] of Object.entries(state.drivers)) {
        if (action.now - d.seen < DRIVER_TTL_MS) drivers[id] = d
        else changed = true
      }
      return changed ? { ...state, arcs, drivers, heat } : state
    }
    case 'message': {
      let msg: Message
      try {
        msg = JSON.parse(action.raw)
      } catch {
        return state
      }
      return reducer(state, { kind: 'event', msg, now: action.now })
    }
    case 'event': {
      const msg = action.msg
      switch (msg.type) {
        case 'driver_moved': {
          const drivers = { ...state.drivers }
          for (const d of msg.data as { id: string; lat: string; lng: string }[]) {
            const pos: [number, number] = [Number(d.lng), Number(d.lat)]
            drivers[d.id] = { id: d.id, pos, prev: state.drivers[d.id]?.pos ?? pos, seen: action.now }
          }
          return { ...state, drivers }
        }
        case 'demand_hex': {
          const demand = { ...state.demand }
          const heat = { ...state.heat }
          for (const d of msg.data as { cell: string; count: number; recent?: number }[]) {
            if (d.count > 0) demand[d.cell] = d.count
            else delete demand[d.cell]
            const added = d.recent ?? d.count
            if (added > 0) {
              const prior = heat[d.cell] ? heatLevel(heat[d.cell], action.now) : 0
              heat[d.cell] = { level: Math.min(HEAT_MAX, prior + added), at: action.now }
            }
          }
          return { ...state, demand, heat }
        }
        case 'matched': {
          const m = toMatch(msg.data as RawMatch, action.now)
          return {
            ...state,
            arcs: [...state.arcs, m],
            recent: [m, ...state.recent].slice(0, RECENT_MAX),
          }
        }
        case 'stats':
          return { ...state, stats: msg.data as Stats }
        default:
          return state
      }
    }
  }
}

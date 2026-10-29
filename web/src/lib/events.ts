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
  seen: number
}

export interface State {
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
  connected: false,
  drivers: {},
  demand: {},
  arcs: [],
  recent: [],
  stats: null,
}

export type Action =
  | { kind: 'connected'; value: boolean }
  | { kind: 'message'; raw: string; now: number }
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
    case 'tick': {
      const arcs = state.arcs.filter((a) => action.now - a.at < ARC_MS)
      const drivers: Record<string, Driver> = {}
      let changed = arcs.length !== state.arcs.length
      for (const [id, d] of Object.entries(state.drivers)) {
        if (action.now - d.seen < DRIVER_TTL_MS) drivers[id] = d
        else changed = true
      }
      return changed ? { ...state, arcs, drivers } : state
    }
    case 'message': {
      let msg: { type: string; data: unknown }
      try {
        msg = JSON.parse(action.raw)
      } catch {
        return state
      }
      switch (msg.type) {
        case 'driver_moved': {
          const drivers = { ...state.drivers }
          for (const d of msg.data as { id: string; lat: string; lng: string }[]) {
            drivers[d.id] = { id: d.id, pos: [Number(d.lng), Number(d.lat)], seen: action.now }
          }
          return { ...state, drivers }
        }
        case 'demand_hex': {
          const demand = { ...state.demand }
          for (const d of msg.data as { cell: string; count: number }[]) {
            if (d.count > 0) demand[d.cell] = d.count
            else delete demand[d.cell]
          }
          return { ...state, demand }
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

import { describe, expect, it } from 'vitest'
import { ARC_MS, arcOpacity, driverPosition, HEAT_COOL_MS, heatLevel, initialState, PING_MS, reducer, slackMinutes } from './events'

const matched = {
  type: 'matched',
  data: {
    request_id: 'r1', rider_id: 'p1', driver_id: 'd1', trip_id: 't1', detour_min: 2.5, pickup_km: 1.2,
    est_arrival_unix: 1000, class_start_unix: 1900, cost_per_person: 3.1, distance_km: 12,
    rider_lat: 49.24, rider_lng: -123.0, driver_lat: 49.23, driver_lng: -123.01,
  },
}

describe('reducer', () => {
  it('adds an arc and a recent match when a match arrives', () => {
    const s = reducer(initialState, { kind: 'message', raw: JSON.stringify(matched), now: 100 })
    expect(s.arcs).toHaveLength(1)
    expect(s.recent[0].rider).toEqual([-123.0, 49.24])
    expect(s.recent[0].driver).toEqual([-123.01, 49.23])
  })

  it('drops arcs older than the fade time on tick but keeps the recent list', () => {
    let s = reducer(initialState, { kind: 'message', raw: JSON.stringify(matched), now: 100 })
    s = reducer(s, { kind: 'tick', now: 100 + ARC_MS + 1 })
    expect(s.arcs).toHaveLength(0)
    expect(s.recent).toHaveLength(1)
  })

  it('parses driver positions and removes stale drivers', () => {
    const moved = { type: 'driver_moved', data: [{ id: 'd1', lat: '49.2', lng: '-123.0', cell: 'x' }] }
    let s = reducer(initialState, { kind: 'message', raw: JSON.stringify(moved), now: 0 })
    expect(s.drivers.d1.pos).toEqual([-123.0, 49.2])
    s = reducer(s, { kind: 'tick', now: 31000 })
    expect(s.drivers.d1).toBeUndefined()
  })

  it('sets and clears demand per hex', () => {
    const up = { type: 'demand_hex', data: [{ cell: 'abc', count: 3 }] }
    const down = { type: 'demand_hex', data: [{ cell: 'abc', count: 0 }] }
    let s = reducer(initialState, { kind: 'message', raw: JSON.stringify(up), now: 0 })
    expect(s.demand.abc).toBe(3)
    s = reducer(s, { kind: 'message', raw: JSON.stringify(down), now: 0 })
    expect(s.demand.abc).toBeUndefined()
  })

  it('ignores malformed messages', () => {
    expect(reducer(initialState, { kind: 'message', raw: 'not json', now: 0 })).toBe(initialState)
  })
})

describe('helpers', () => {
  it('computes minutes of slack before class', () => {
    expect(slackMinutes({ estArrivalUnix: 1000, classStartUnix: 1900 })).toBe(15)
  })
  it('fades arcs linearly', () => {
    expect(arcOpacity(0)).toBe(1)
    expect(arcOpacity(ARC_MS / 2)).toBeCloseTo(0.5)
    expect(arcOpacity(ARC_MS * 2)).toBe(0)
  })
})

describe('driver movement', () => {
  it('glides from the previous ping to the latest one', () => {
    const at = (lng: number, now: number) =>
      reducer(initialState, { kind: 'event', msg: { type: 'driver_moved', data: [{ id: 'd1', lat: '49', lng: String(lng) }] }, now })
    const first = at(-123.0, 0)
    const second = reducer(first, { kind: 'event', msg: { type: 'driver_moved', data: [{ id: 'd1', lat: '49', lng: '-122.0' }] }, now: 1000 })
    const d = second.drivers.d1
    expect(driverPosition(d, 1000)[0]).toBeCloseTo(-123.0)
    expect(driverPosition(d, 1000 + PING_MS / 2)[0]).toBeCloseTo(-122.5)
    expect(driverPosition(d, 1000 + PING_MS * 3)[0]).toBeCloseTo(-122.0)
  })

  it('reset clears the map but keeps the connection flag', () => {
    const moved = reducer(initialState, { kind: 'event', msg: { type: 'driver_moved', data: [{ id: 'd1', lat: '49', lng: '-123' }] }, now: 0 })
    const s = reducer({ ...moved, connected: true }, { kind: 'reset' })
    expect(Object.keys(s.drivers)).toHaveLength(0)
    expect(s.connected).toBe(true)
  })
})

describe('demand heat', () => {
  const up = (now: number, state = initialState) =>
    reducer(state, { kind: 'event', msg: { type: 'demand_hex', data: [{ cell: 'abc', count: 1 }] }, now })

  it('rises with repeated requests in a hexagon and cools over time', () => {
    const s = up(1000, up(0))
    expect(heatLevel(s.heat.abc, 1000)).toBeCloseTo(2 - 1000 / HEAT_COOL_MS + 1 - 1, 1)
    expect(heatLevel(s.heat.abc, 1000 + HEAT_COOL_MS * 5)).toBe(0)
  })

  it('drops fully cooled hexagons on tick', () => {
    const s = reducer(up(0), { kind: 'tick', now: HEAT_COOL_MS * 3 })
    expect(s.heat.abc).toBeUndefined()
  })
})

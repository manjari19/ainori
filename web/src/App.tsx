import { useEffect, useMemo, useReducer, useState } from 'react'
import { DeckGL, ArcLayer, H3HexagonLayer, ScatterplotLayer } from 'deck.gl'
import { Map } from 'react-map-gl/maplibre'
import 'maplibre-gl/dist/maplibre-gl.css'
import {
  ARC_MS, arcOpacity, initialState, reducer, slackMinutes,
  type Match,
} from './lib/events'

const API = import.meta.env.VITE_API_URL ?? 'http://localhost:8080'
const WS_URL = API.replace(/^http/, 'ws') + '/ws'
const SFU: [number, number] = [-122.9199, 49.2781]
const MAP_STYLE = 'https://tiles.openfreemap.org/styles/positron'

// Cool to warm: more open requests in a hexagon means a warmer colour.
const HEAT: [number, number, number][] = [
  [90, 160, 220], [90, 200, 200], [150, 210, 120], [240, 200, 70], [245, 140, 50], [220, 60, 50],
]
function heat(count: number): [number, number, number, number] {
  const c = HEAT[Math.min(HEAT.length - 1, Math.max(0, count - 1))]
  return [c[0], c[1], c[2], 190]
}

function clock(unix: number) {
  return new Date(unix * 1000).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}

export default function App() {
  const [state, dispatch] = useReducer(reducer, initialState)
  const [now, setNow] = useState(() => Date.now())
  const [selected, setSelected] = useState<Match | null>(null)
  const [demoMsg, setDemoMsg] = useState('')

  // WebSocket with automatic reconnect.
  useEffect(() => {
    let ws: WebSocket | null = null
    let retry: ReturnType<typeof setTimeout>
    let closed = false
    const open = () => {
      ws = new WebSocket(WS_URL)
      ws.onopen = () => dispatch({ kind: 'connected', value: true })
      ws.onmessage = (e) => dispatch({ kind: 'message', raw: String(e.data), now: Date.now() })
      ws.onclose = () => {
        dispatch({ kind: 'connected', value: false })
        if (!closed) retry = setTimeout(open, 1500)
      }
    }
    open()
    return () => {
      closed = true
      clearTimeout(retry)
      ws?.close()
    }
  }, [])

  // 100ms clock drives arc fading and stale driver cleanup.
  useEffect(() => {
    const id = setInterval(() => {
      const t = Date.now()
      setNow(t)
      dispatch({ kind: 'tick', now: t })
    }, 100)
    return () => clearInterval(id)
  }, [])

  const layers = useMemo(() => {
    const demand = Object.entries(state.demand).map(([cell, count]) => ({ cell, count }))
    return [
      new H3HexagonLayer({
        id: 'demand', data: demand, getHexagon: (d: { cell: string }) => d.cell,
        getFillColor: (d: { count: number }) => heat(d.count), extruded: false, pickable: false,
        updateTriggers: { getFillColor: demand.map((d) => d.count).join(',') },
      }),
      new ScatterplotLayer({
        id: 'drivers', data: Object.values(state.drivers), getPosition: (d: { pos: [number, number] }) => d.pos,
        getFillColor: [30, 90, 200], getLineColor: [255, 255, 255], stroked: true, lineWidthMinPixels: 1,
        radiusMinPixels: 4, radiusMaxPixels: 7, getRadius: 40,
      }),
      new ArcLayer<Match>({
        id: 'matches', data: state.arcs, getSourcePosition: (m) => m.rider, getTargetPosition: (m) => m.driver,
        getSourceColor: (m) => [230, 90, 40, 255 * arcOpacity(now - m.at)],
        getTargetColor: (m) => [30, 90, 200, 255 * arcOpacity(now - m.at)],
        getWidth: 4, updateTriggers: { getSourceColor: now, getTargetColor: now },
      }),
      new ScatterplotLayer({
        id: 'sfu', data: [{ pos: SFU }], getPosition: (d: { pos: [number, number] }) => d.pos,
        getFillColor: [166, 25, 46], radiusMinPixels: 8, getRadius: 120, stroked: true,
        getLineColor: [255, 255, 255], lineWidthMinPixels: 2,
      }),
    ]
  }, [state.demand, state.drivers, state.arcs, now])

  async function runDemo() {
    setDemoMsg('Starting…')
    try {
      const res = await fetch(API + '/demo/start', { method: 'POST' })
      const body = await res.json().catch(() => ({}))
      setDemoMsg(res.ok ? 'Demo running for about 2 minutes.' : (body.error ?? `Could not start (${res.status})`))
    } catch {
      setDemoMsg('Could not reach the server.')
    }
  }

  const s = state.stats
  const reasons = Object.entries(s?.unmatched_reasons ?? {})
  const fadeSec = ARC_MS / 1000

  return (
    <div className="app">
      <div className="map">
        <DeckGL initialViewState={{ longitude: -122.95, latitude: 49.25, zoom: 10.6 }} controller layers={layers}>
          <Map mapStyle={MAP_STYLE} />
        </DeckGL>
      </div>

      <aside className="panel">
        <header>
          <h1>Ainori</h1>
          <span className={state.connected ? 'dot on' : 'dot'} title={state.connected ? 'Live' : 'Reconnecting'} />
        </header>
        <p className="sub">Live carpool matching to SFU Burnaby</p>

        <div className="counters">
          <div><b>{s?.drivers_online ?? 0}</b><span>drivers online</span></div>
          <div><b>{s?.open_requests ?? 0}</b><span>open requests</span></div>
          <div><b>{s ? Math.round(s.match_rate * 100) : 0}%</b><span>match rate</span></div>
          <div><b>{s ? Math.round(s.p95_ms) : 0} ms</b><span>p95 latency</span></div>
        </div>

        <button className="demo" onClick={runDemo}>Run demo</button>
        {demoMsg && <p className="note">{demoMsg}</p>}

        <h2>Why riders went unmatched</h2>
        {reasons.length === 0 ? <p className="note">None yet.</p> : (
          <ul className="reasons">
            {reasons.map(([k, v]) => <li key={k}><span>{k.replaceAll('_', ' ')}</span><b>{v}</b></li>)}
          </ul>
        )}

        <h2>Recent matches</h2>
        <p className="note">Arcs fade after {fadeSec} seconds. Tap a match for details.</p>
        <ul className="recent">
          {state.recent.slice(0, 8).map((m) => (
            <li key={m.requestId}>
              <button onClick={() => setSelected(m)} className={selected?.requestId === m.requestId ? 'sel' : ''}>
                Rider matched, ${m.costPerPerson.toFixed(2)} each
              </button>
            </li>
          ))}
        </ul>

        {selected && (
          <div className="detail">
            <h3>Match details</h3>
            <dl>
              <dt>Detour</dt><dd>{selected.detourMin.toFixed(1)} min</dd>
              <dt>Pickup distance</dt><dd>{selected.pickupKm.toFixed(1)} km</dd>
              <dt>Arrives</dt><dd>{clock(selected.estArrivalUnix)}</dd>
              <dt>Class starts</dt><dd>{clock(selected.classStartUnix)}</dd>
              <dt>Time to spare</dt><dd>{slackMinutes(selected)} min</dd>
              <dt>Trip distance</dt><dd>{selected.distanceKm.toFixed(1)} km</dd>
              <dt>Gas cost each</dt><dd>${selected.costPerPerson.toFixed(2)}</dd>
            </dl>
          </div>
        )}
      </aside>
    </div>
  )
}

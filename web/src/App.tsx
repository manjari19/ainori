import { useEffect, useMemo, useReducer, useState } from 'react'
import { DeckGL, ArcLayer, H3HexagonLayer, ScatterplotLayer } from 'deck.gl'
import { Map } from 'react-map-gl/maplibre'
import { setWorkerUrl } from 'maplibre-gl'
import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?url'
import 'maplibre-gl/dist/maplibre-gl.css'
import {
  ARC_MS, arcOpacity, driverPosition, heatLevel, initialState, reducer, slackMinutes,
  type Driver, type Match,
} from './lib/events'
import { loadRecording, startReplay, type Recording } from './lib/replay'

// The bundler does not emit MapLibre's worker file by default, so the production build
// had no basemap. Ship it as an asset and point MapLibre at it.
setWorkerUrl(new URL(workerUrl, window.location.href).href)

// In development the backend is expected on localhost. In a production build there is
// no backend unless VITE_API_URL says so, and the map replays a recording instead.
const API: string = import.meta.env.VITE_API_URL ?? (import.meta.env.DEV ? 'http://localhost:8080' : '')
const WS_URL = API.replace(/^http/, 'ws') + '/ws'
const REPO_URL = 'https://github.com/manjari19/ainori'
const RECORDING_URL = `${import.meta.env.BASE_URL}demo/recording.json`
const SFU: [number, number] = [-122.9199, 49.2781]
const MAP_STYLE = 'https://tiles.openfreemap.org/styles/positron'
const LIVE_TIMEOUT_MS = 3000
const LIVE_RETRY_MS = 10000

// Cool to warm: more open requests in a hexagon means a warmer colour.
const HEAT: [number, number, number][] = [
  [90, 160, 220], [90, 200, 200], [150, 210, 120], [240, 200, 70], [245, 140, 50], [220, 60, 50],
]
function heat(count: number): [number, number, number, number] {
  const c = HEAT[Math.min(HEAT.length - 1, Math.max(0, count - 1))]
  return [c[0], c[1], c[2], 190]
}
const heatCss = `linear-gradient(90deg, ${HEAT.map((c) => `rgb(${c.join(',')})`).join(', ')})`

function clock(unix: number) {
  return new Date(unix * 1000).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}

type Mode = 'connecting' | 'live' | 'replay' | 'offline'

function Legend() {
  return (
    <ul className="legend" aria-label="Map legend">
      <li>
        <i className="sw heat" style={{ background: heatCss }} />
        <span>Hexagon: recent ride requests (few to many)</span>
      </li>
      <li><i className="sw dot driver" /><span>Driver</span></li>
      <li><i className="sw arc" /><span>Match: rider to driver</span></li>
      <li><i className="sw dot sfu" /><span>SFU Burnaby</span></li>
    </ul>
  )
}

export default function App() {
  const [state, dispatch] = useReducer(reducer, initialState)
  const [now, setNow] = useState(() => Date.now())
  const [selected, setSelected] = useState<Match | null>(null)
  const [demoMsg, setDemoMsg] = useState('')
  const [mode, setMode] = useState<Mode>('connecting')

  // Try the live backend. If it is not reachable, replay the recording and keep retrying
  // in the background so a backend that comes up later takes over.
  useEffect(() => {
    let cancelled = false
    let sock: WebSocket | null = null
    let retry: ReturnType<typeof setTimeout> | undefined
    let stopReplay: (() => void) | null = null
    let recording: Recording | null | undefined

    const beginReplay = async () => {
      if (stopReplay) return
      if (recording === undefined) recording = await loadRecording(RECORDING_URL)
      if (cancelled || stopReplay) return
      if (!recording) {
        setMode('offline')
        return
      }
      dispatch({ kind: 'reset' })
      setMode('replay')
      stopReplay = startReplay(
        recording,
        (msg) => dispatch({ kind: 'event', msg, now: Date.now() }),
        () => dispatch({ kind: 'reset' }),
      )
    }

    const tryLive = () => {
      if (cancelled) return
      if (!API) {
        void beginReplay()
        return
      }
      const s = new WebSocket(WS_URL)
      sock = s
      const giveUp = setTimeout(() => s.close(), LIVE_TIMEOUT_MS)
      s.onopen = () => {
        clearTimeout(giveUp)
        stopReplay?.()
        stopReplay = null
        dispatch({ kind: 'reset' })
        dispatch({ kind: 'connected', value: true })
        setMode('live')
      }
      s.onmessage = (e) => dispatch({ kind: 'message', raw: String(e.data), now: Date.now() })
      s.onclose = () => {
        clearTimeout(giveUp)
        dispatch({ kind: 'connected', value: false })
        if (cancelled) return
        void beginReplay()
        retry = setTimeout(tryLive, LIVE_RETRY_MS)
      }
    }
    tryLive()
    return () => {
      cancelled = true
      clearTimeout(retry)
      stopReplay?.()
      sock?.close()
    }
  }, [])

  // 100ms clock drives arc fading, smooth driver movement and stale driver cleanup.
  useEffect(() => {
    const id = setInterval(() => {
      const t = Date.now()
      setNow(t)
      dispatch({ kind: 'tick', now: t })
    }, 100)
    return () => clearInterval(id)
  }, [])

  const layers = useMemo(() => {
    const demand = Object.entries(state.heat).map(([cell, h]) => ({ cell, count: Math.ceil(heatLevel(h, now)) }))
    return [
      new H3HexagonLayer({
        id: 'demand', data: demand, getHexagon: (d: { cell: string }) => d.cell,
        getFillColor: (d: { count: number }) => heat(d.count), extruded: false, pickable: false,
        updateTriggers: { getFillColor: demand.map((d) => d.count).join(',') },
      }),
      new ScatterplotLayer<Driver>({
        id: 'drivers', data: Object.values(state.drivers), getPosition: (d) => driverPosition(d, now),
        getFillColor: [30, 90, 200], getLineColor: [255, 255, 255], stroked: true, lineWidthMinPixels: 1,
        radiusMinPixels: 4, radiusMaxPixels: 7, getRadius: 40,
        updateTriggers: { getPosition: now },
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
  }, [state.heat, state.drivers, state.arcs, now])

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
  const pct = s ? `${Math.round(s.match_rate * 100)}%` : '0%'
  const ms = (v?: number) => `${Math.round(v ?? 0)} ms`

  return (
    <div className="app">
      <header className="top">
        <div className="brand">
          <h1>Ainori <span lang="ja">相乗り</span></h1>
          <p>Real-time carpool matching for SFU. Riders get a driver heading up Burnaby Mountain in milliseconds.</p>
        </div>
        <a className="gh" href={REPO_URL} target="_blank" rel="noreferrer">
          <svg viewBox="0 0 16 16" width="18" height="18" aria-hidden="true">
            <path fill="currentColor" d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
          </svg>
          <span>GitHub</span>
        </a>
      </header>

      <div className="body">
        <div className="map">
          <DeckGL initialViewState={{ longitude: -122.95, latitude: 49.25, zoom: 10.6 }} controller layers={layers}>
            <Map mapStyle={MAP_STYLE} />
          </DeckGL>
          <div className={`badge ${mode}`} role="status">
            {mode === 'live' && 'Live'}
            {mode === 'replay' && 'Replay of a recorded simulation'}
            {mode === 'connecting' && 'Connecting…'}
            {mode === 'offline' && 'No backend and no recording found'}
          </div>
          <div className="legend-card desktop-only"><Legend /></div>
        </div>

        <aside className="panel">
          <section>
            <h2>Live stats</h2>
            <div className="counters">
              <div><b>{pct}</b><span>match rate</span></div>
              <div><b>{ms(s?.p95_ms)}</b><span>p95 latency</span></div>
              <div><b>{ms(s?.p99_ms)}</b><span>p99 latency</span></div>
              <div><b>{s?.drivers_online ?? 0}</b><span>drivers online</span></div>
              <div><b>{s?.open_requests ?? 0}</b><span>open requests</span></div>
              <div><b>{s?.matched ?? 0}</b><span>riders matched</span></div>
            </div>
            {mode === 'replay' && (
              <p className="note">
                No backend is connected, so this replays a real 2-minute simulation: 450 drivers and 1,200
                ride requests at 10 per second, recorded from the running system. Latency is measured at the gateway.
              </p>
            )}
            {mode === 'live' && (
              <>
                <button className="demo" onClick={runDemo}>Run demo</button>
                {demoMsg && <p className="note">{demoMsg}</p>}
              </>
            )}
          </section>

          <section className="mobile-only">
            <h2>Legend</h2>
            <Legend />
          </section>

          <section>
            <h2>Why riders went unmatched</h2>
            {reasons.length === 0 ? <p className="note">None yet.</p> : (
              <ul className="reasons">
                {reasons.map(([k, v]) => <li key={k}><span>{k.replaceAll('_', ' ')}</span><b>{v}</b></li>)}
              </ul>
            )}
          </section>

          <section>
            <h2>Recent matches</h2>
            <p className="note">Arcs fade after {ARC_MS / 1000} seconds. Tap a match for details.</p>
            <ul className="recent">
              {state.recent.slice(0, 6).map((m) => (
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
          </section>

          <footer>Go, gRPC, Redis Streams, H3, Postgres, OpenTelemetry, React, deck.gl</footer>
        </aside>
      </div>
    </div>
  )
}

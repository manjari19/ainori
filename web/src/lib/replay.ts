import type { Message } from './events'

export interface Recording {
  version: number
  durationMs: number
  events: [number, Message][] // [ms since start, same message the WebSocket sends]
}

// Index of the first event that is not yet due at `elapsedMs`, starting from `from`.
export function nextIndex(events: Recording['events'], from: number, elapsedMs: number): number {
  let i = from
  while (i < events.length && events[i][0] <= elapsedMs) i++
  return i
}

export const LOOP_PAUSE_MS = 4000

export async function loadRecording(url: string): Promise<Recording | null> {
  try {
    const res = await fetch(url)
    if (!res.ok) return null
    const rec = (await res.json()) as Recording
    return rec.events?.length ? rec : null
  } catch {
    return null
  }
}

// Plays the recording on a loop. `onEvent` gets each message when it is due, and `onLoop`
// is called before every repeat so the caller can clear the map. Returns a stop function.
export function startReplay(
  rec: Recording,
  onEvent: (msg: Message) => void,
  onLoop: () => void,
): () => void {
  let started = performance.now()
  let idx = 0
  const timer = setInterval(() => {
    const elapsed = performance.now() - started
    const next = nextIndex(rec.events, idx, elapsed)
    for (let i = idx; i < next; i++) onEvent(rec.events[i][1])
    idx = next
    if (elapsed > rec.durationMs + LOOP_PAUSE_MS) {
      onLoop()
      started = performance.now()
      idx = 0
    }
  }, 50)
  return () => clearInterval(timer)
}

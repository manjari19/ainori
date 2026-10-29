import { describe, expect, it } from 'vitest'
import { nextIndex, type Recording } from './replay'

const events: Recording['events'] = [
  [0, { type: 'a', data: 1 }],
  [100, { type: 'b', data: 2 }],
  [100, { type: 'c', data: 3 }],
  [500, { type: 'd', data: 4 }],
]

describe('nextIndex', () => {
  it('returns nothing due before the first event time', () => {
    expect(nextIndex(events, 0, -1)).toBe(0)
  })
  it('returns every event that is due, including ties', () => {
    expect(nextIndex(events, 0, 100)).toBe(3)
  })
  it('continues from where it left off', () => {
    expect(nextIndex(events, 3, 200)).toBe(3)
    expect(nextIndex(events, 3, 500)).toBe(4)
  })
})

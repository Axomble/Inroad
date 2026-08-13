import { describe, expect, test } from 'vitest'
import { healthMeta } from './warmup-health'
import { laneMeta, toWarmupLane } from './warmup-lane'

describe('warmup lane mapping', () => {
  // Every lane maps to distinct operator copy, so the lane is readable from text
  // alone (color is redundant reinforcement) — five severity tones cover seven
  // lanes on purpose, and the label is what tells them apart.
  test.each([
    ['pending_auth', 'Awaiting DNS', 'text-muted-foreground'],
    ['probation', 'Proving', 'text-warm'],
    ['healthy', 'Healthy', 'text-ok'],
    ['watch', 'Watch', 'text-warn'],
    ['recovery', 'Recovering', 'text-warm'],
    ['quarantine', 'Withheld', 'text-danger'],
    ['blocked', 'Blocked', 'text-danger'],
  ] as const)('%s -> %s / %s', (lane, label, text) => {
    const meta = laneMeta[lane]
    expect(meta.label).toBe(label)
    expect(meta.text).toBe(text)
  })

  test('every lane has its own label, so no two lanes read the same', () => {
    const labels = Object.values(laneMeta).map((m) => m.label)
    expect(new Set(labels).size).toBe(labels.length)
  })

  // The whole point of the lane axis is that being withheld from the pool is a
  // harder fact than not having proven yourself yet: quarantine/blocked carry
  // the danger tone, probation/recovery only the warmup "heat" tone.
  test('withheld lanes carry a more severe tone than lanes that are merely unproven', () => {
    expect(laneMeta.quarantine.text).toBe('text-danger')
    expect(laneMeta.blocked.text).toBe('text-danger')
    expect(laneMeta.probation.text).not.toBe('text-danger')
    expect(laneMeta.recovery.text).not.toBe('text-danger')
    expect(laneMeta.pending_auth.text).not.toBe('text-danger')
  })

  // Absence of a lane is not evidence of pool membership. An older server, a
  // dropped field or a lane this build has never heard of must land on the
  // unproven lane, never on the one that says "exchanging mail with healthy
  // peers" (mirrors toWarmupHealth's fallback to `unknown`).
  test('an unrecognized or absent lane falls back to probation, never healthy', () => {
    for (const value of ['bogus', 'sentinel', '', null, undefined]) {
      expect(toWarmupLane(value)).toBe('probation')
      expect(toWarmupLane(value)).not.toBe('healthy')
    }
  })

  test('a known lane string narrows to itself', () => {
    expect(toWarmupLane('quarantine')).toBe('quarantine')
    expect(toWarmupLane('pending_auth')).toBe('pending_auth')
    expect(toWarmupLane('healthy')).toBe('healthy')
  })

  // Reputation and pool eligibility are independent axes whose vocabularies
  // only partly overlap (`healthy`/`watch`). Nothing may treat one map as a
  // lookup for the other, so they stay separate records — a reputation state is
  // not a valid lane and vice versa.
  test('the two axes are separate maps, not one vocabulary', () => {
    expect(Object.keys(laneMeta)).not.toEqual(Object.keys(healthMeta))
    expect('throttled' in laneMeta).toBe(false)
    expect('paused' in laneMeta).toBe(false)
    expect('probation' in healthMeta).toBe(false)
    expect('quarantine' in healthMeta).toBe(false)
    // The overlapping names must not narrow across axes.
    expect(toWarmupLane('throttled')).toBe('probation')
    expect(toWarmupLane('paused')).toBe('probation')
    expect(toWarmupLane('unknown')).toBe('probation')
  })
})

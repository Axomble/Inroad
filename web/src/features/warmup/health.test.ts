import { describe, expect, test } from 'vitest'
import { healthMeta, toWarmupHealth } from './health'

describe('warmup health mapping', () => {
  // Each state maps to a distinct label AND a distinct color token, so the four
  // states are tellable apart by text alone (color is redundant reinforcement).
  test.each([
    ['healthy', 'Healthy', 'text-ok', 'bg-ok'],
    ['watch', 'Watch', 'text-warn', 'bg-warn'],
    ['throttled', 'Throttled', 'text-warm', 'bg-warm'],
    ['paused', 'Paused', 'text-danger', 'bg-danger'],
  ] as const)('%s -> %s / %s / %s', (state, label, text, dot) => {
    const meta = healthMeta[state]
    expect(meta.label).toBe(label)
    expect(meta.text).toBe(text)
    expect(meta.dot).toBe(dot)
  })

  test('every state has a unique color token (no two states share a hue)', () => {
    const texts = Object.values(healthMeta).map((m) => m.text)
    const dots = Object.values(healthMeta).map((m) => m.dot)
    expect(new Set(texts).size).toBe(texts.length)
    expect(new Set(dots).size).toBe(dots.length)
  })

  test('an unknown/absent state falls back to healthy rather than a blank badge', () => {
    expect(toWarmupHealth('bogus')).toBe('healthy')
    expect(toWarmupHealth(null)).toBe('healthy')
    expect(toWarmupHealth(undefined)).toBe('healthy')
  })

  test('a known state string narrows to itself', () => {
    expect(toWarmupHealth('throttled')).toBe('throttled')
  })
})

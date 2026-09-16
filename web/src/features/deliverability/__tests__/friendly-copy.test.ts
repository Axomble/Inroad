import { describe, expect, test } from 'vitest'
import type { DeliverabilityScore } from '@/store/api'
import { friendlyComponentCopies, friendlyScoreHeadline } from '../friendly-copy'

function score(overrides: Partial<DeliverabilityScore> = {}): DeliverabilityScore {
  return {
    value: 74,
    confidence: 'high',
    delivered: 4_120,
    components: [
      { key: 'bounce', label: 'Bounces', penalty: 12, rate: 3.4, measured: true },
      { key: 'complaint', label: 'Complaints', penalty: 0, rate: null, measured: false },
    ],
    ...overrides,
  }
}

describe('friendlyScoreHeadline', () => {
  test('a low-confidence score reads as an early estimate, keeping the honest sample size', () => {
    const headline = friendlyScoreHeadline(score({ value: 96, confidence: 'low', delivered: 11 }))
    expect(headline.provisional).toBe(true)
    expect(headline.label).toBe('Early estimate')
    // Still faint, never a verdict colour — the lib's judgement is untouched.
    expect(headline.tone).toBe('draft')
    expect(headline.qualifier).toMatch(/haven't sent enough email yet for a reliable score/)
    expect(headline.qualifier).toContain('based on just 11 delivered')
    expect(headline.qualifier).toMatch(/firm up as you send more/)
  })

  test('a confident score passes through the shared copy unchanged', () => {
    const headline = friendlyScoreHeadline(score())
    expect(headline.provisional).toBe(false)
    expect(headline.label).toBe('Watch')
    expect(headline.qualifier).toContain('Computed over 4,120 delivered')
  })
})

describe('friendlyComponentCopies', () => {
  test('an unmeasured component reads "No data yet" with a plain reason, never a healthy tone', () => {
    const [, complaints] = friendlyComponentCopies(score())
    expect(complaints?.measured).toBe(false)
    expect(complaints?.status).toBe('No data yet')
    expect(complaints?.tone).toBe('draft')
    expect(complaints?.detail).toMatch(/Complaint reports aren't connected yet/)
    expect(complaints?.detail).toMatch(/doesn't mean your complaint rate is clean/)
  })

  test("a server-written detail wins over the local plain-language fallback", () => {
    const [, complaints] = friendlyComponentCopies(
      score({
        components: [
          { key: 'bounce', label: 'Bounces', penalty: 12, rate: 3.4, measured: true },
          {
            key: 'complaint',
            label: 'Complaints',
            penalty: 0,
            rate: null,
            measured: false,
            detail: 'The feed disconnected on 3 Sep.',
          },
        ],
      }),
    )
    expect(complaints?.status).toBe('No data yet')
    expect(complaints?.detail).toBe('The feed disconnected on 3 Sep.')
  })

  test('a measured component is passed through untouched', () => {
    const [bounces] = friendlyComponentCopies(score())
    expect(bounces?.measured).toBe(true)
    expect(bounces?.status).toBe('3.4% — costing 12 points')
    expect(bounces?.penaltyLabel).toBe('−12 points')
  })
})

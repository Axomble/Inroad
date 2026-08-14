import { describe, expect, test } from 'vitest'
import type { WarmupTransition } from '@/store/api'
import { evidenceRows, healthChange, laneChange, laneReasonCopy, reasonCopy } from './transition-copy'

/** A transition with no evidence and no movement; each test varies what it means to. */
function transition(overrides: Partial<WarmupTransition> = {}): WarmupTransition {
  return {
    id: 't-1',
    created_at: '2026-08-12T10:00:00Z',
    from_state: 'healthy',
    to_state: 'healthy',
    reason_code: 'health_unchanged',
    reason: 'health is unchanged; this transition moves the pool lane',
    placement_samples: 0,
    spam_rate: 0,
    bounce_samples: 0,
    bounce_rate: 0,
    complaint_samples: 0,
    complaint_rate: 0,
    invalid_tokens: 0,
    policy_version: 'warmup-phase1-v1',
    ...overrides,
  }
}

describe('reason copy', () => {
  test('a known reason code reads as a sentence, never as the raw code', () => {
    const copy = reasonCopy('spam_pause', 'spam placement rate above the pause threshold')
    expect(copy).not.toContain('spam_pause')
    expect(copy).not.toContain('_')
    expect(copy.toLowerCase()).toContain('spam placement')
    expect(copy.toLowerCase()).toContain('pause')
  })

  // Each code carries its own sentence: an operator reading two rows must be able
  // to tell "held because the sample was thin" from "recovered one step".
  test('codes that mean different things do not share copy', () => {
    const codes = [
      'spam_watch',
      'spam_throttle',
      'spam_pause',
      'campaign_bounce_pause',
      'warmup_bounce_pause',
      'complaint_pause',
      'placement_sample_insufficient',
      'insufficient_evidence_to_recover',
      'recovery_step',
      'recovery_blocked_by_dwell',
      'evidence_qualified',
      'health_unchanged',
    ]
    const sentences = codes.map((code) => reasonCopy(code, ''))
    expect(new Set(sentences).size).toBe(codes.length)
  })

  // A code from a newer policy than this build: the server's own prose is the
  // best copy available, and the machine token must not leak into the UI.
  test('an unknown code falls back to the server sentence', () => {
    expect(reasonCopy('spam_incinerated', 'the mailbox was set on fire')).toBe('the mailbox was set on fire')
  })

  test('an unknown code with no server sentence is humanised, not printed raw', () => {
    expect(reasonCopy('spam_incinerated', '')).toBe('Spam incinerated')
  })

  // The lane axis has its own explanation, and rows that moved only the health
  // axis have none at all — that is an absence, not empty copy to render.
  test('a row with no lane reason has no lane copy', () => {
    expect(laneReasonCopy(null, null)).toBeNull()
    expect(laneReasonCopy(undefined, undefined)).toBeNull()
    expect(laneReasonCopy('', '')).toBeNull()
  })

  test('a known lane reason code reads as a sentence', () => {
    const copy = laneReasonCopy('lane_quarantined', 'quarantined: spam placement rate above the pause threshold')
    expect(copy).not.toBeNull()
    expect(copy).not.toContain('lane_quarantined')
    expect(copy?.toLowerCase()).toContain('withheld')
  })
})

describe('the two axes', () => {
  test('a reputation move reports both ends', () => {
    const change = healthChange(transition({ from_state: 'healthy', to_state: 'throttled' }))
    expect(change).toEqual({ kind: 'moved', from: 'healthy', to: 'throttled' })
  })

  test('a row that only moved the lane reports reputation as unchanged', () => {
    const change = healthChange(transition({ from_state: 'watch', to_state: 'watch' }))
    expect(change).toEqual({ kind: 'unchanged', state: 'watch' })
  })

  test('a lane move reports both ends', () => {
    const change = laneChange(transition({ from_lane: 'probation', to_lane: 'healthy' }))
    expect(change).toEqual({ kind: 'moved', from: 'probation', to: 'healthy' })
  })

  test('a row whose lane did not move reports it as unchanged, not as a move', () => {
    expect(laneChange(transition({ from_lane: 'watch', to_lane: 'watch' }))).toEqual({
      kind: 'unchanged',
      lane: 'watch',
    })
  })

  // Rows written before pool lanes existed carry null on both lane fields. That
  // is history without a lane — never an error, and never a fabricated lane
  // (defaulting to probation here would invent a fact about the past).
  test('a pre-lane row reports the lane as unrecorded rather than defaulting it', () => {
    expect(laneChange(transition({ from_lane: null, to_lane: null }))).toEqual({ kind: 'unrecorded' })
    expect(laneChange(transition())).toEqual({ kind: 'unrecorded' })
  })

  // A lane value this build has never heard of is present, so it is not
  // "unrecorded" — and it must still refuse to read as the healthy pool.
  test('an unreadable lane value is narrowed to the unproven lane, never to healthy', () => {
    const change = laneChange(
      transition({ from_lane: 'healthy', to_lane: 'sentinel' as WarmupTransition['to_lane'] }),
    )
    expect(change).toEqual({ kind: 'moved', from: 'healthy', to: 'probation' })
  })
})

describe('evidence readings', () => {
  /** The row for one metric, by its operator-facing label. */
  function row(t: WarmupTransition, label: string) {
    const found = evidenceRows(t).find((r) => r.label === label)
    if (!found) throw new Error(`no evidence row labelled ${label}`)
    return found
  }

  // THE misleading case. 3 spam in 5 sends is below the policy's minimum sample,
  // so the API reports spam_rate 0 — a floor, not a measurement. Printing "0%"
  // next to a mailbox that spammed 60% of its sample is the single worst thing
  // this panel could do.
  test('a lower-bounded zero over a real sample never renders as a clean zero', () => {
    const spam = row(transition({ placement_samples: 5, spam_rate: 0 }), 'Spam placement')

    expect(spam.proven).toBe(false)
    expect(spam.value).not.toMatch(/0\s*%/)
    expect(spam.value).not.toBe('0%')
    expect(spam.detail).toMatch(/5 observations/)
    // The sentence has to say why 0 is not a measurement, or the number is back.
    expect(spam.detail.toLowerCase()).toMatch(/minimum|floor/)
  })

  // No sample at all is a different fact from an unproven one, and neither is 0%.
  test('an empty window reads as nothing observed, not as a rate', () => {
    const spam = row(transition({ placement_samples: 0, spam_rate: 0 }), 'Spam placement')
    expect(spam.proven).toBe(false)
    expect(spam.value.toLowerCase()).toContain('no observations')
    expect(spam.value).not.toContain('%')
  })

  // A non-zero rate is still a bound, not the observed share, so it is rendered
  // with its direction ("at least") and never as a flat percentage.
  test('a non-zero rate is presented as a lower bound with its sample', () => {
    const complaints = row(transition({ complaint_samples: 1000, complaint_rate: 0.013 }), 'Complaints')

    expect(complaints.proven).toBe(true)
    expect(complaints.value).toBe('at least 1.3%')
    expect(complaints.detail).toContain('1,000 observations')
    expect(complaints.detail.toLowerCase()).toContain('95%')
  })

  // Complaint thresholds live at fractions of a percent, so one decimal place
  // would round a real signal back to the "0.0%" this module exists to prevent.
  test('a sub-one-percent bound keeps the precision its threshold needs', () => {
    const complaints = row(transition({ complaint_samples: 1000, complaint_rate: 0.0018 }), 'Complaints')
    expect(complaints.value).toBe('at least 0.18%')
  })

  test('every metric names its own sample count', () => {
    const rows = evidenceRows(
      transition({
        placement_samples: 40,
        spam_rate: 0.2,
        bounce_samples: 200,
        bounce_rate: 0.06,
        complaint_samples: 1000,
        complaint_rate: 0.013,
      }),
    )
    expect(rows.map((r) => r.samples)).toEqual([40, 200, 1000])
    expect(rows.every((r) => r.proven)).toBe(true)
  })

  // Forged tokens are observer-side: they say something about mail this mailbox
  // RECEIVED and are never attributed to it as a sender, so the row must not sit
  // silently among the rates that do gate health.
  test('received forged tokens are labelled as never counting against the sender', () => {
    const rows = evidenceRows(transition({ invalid_tokens: 4 }))
    const tokens = rows.find((r) => r.label === 'Forged tokens received')
    expect(tokens?.value).toBe('4')
    expect(tokens?.detail.toLowerCase()).toContain('never')
  })

  test('no forged tokens means no row for them', () => {
    expect(evidenceRows(transition({ invalid_tokens: 0 })).some((r) => r.label === 'Forged tokens received')).toBe(
      false,
    )
  })
})

describe('bounce population', () => {
  function bounceRow(overrides: Partial<WarmupTransition>) {
    const found = evidenceRows(transition(overrides)).find((r) => /hard bounces/i.test(r.label))
    if (!found) throw new Error('no hard-bounce evidence row')
    return found
  }

  // The two populations are kept apart because pooling them let synthetic warmup
  // traffic dilute a real campaign bounce rate below its own threshold. A row that
  // does not say which one it counted hands the operator a number they cannot
  // attribute — and "campaign hard bounces crossed the threshold" beside a
  // warmup denominator reads as a contradiction.
  it('names the campaign population and says whose mail it counted', () => {
    const r = bounceRow({ bounce_population: 'campaign', bounce_rate: 0.12, bounce_samples: 200 })
    expect(r.label).toMatch(/campaign/i)
    expect(r.detail).toMatch(/real recipients/i)
  })

  it('names the warmup population and marks it synthetic', () => {
    const r = bounceRow({ bounce_population: 'warmup', bounce_rate: 0.12, bounce_samples: 200 })
    expect(r.label).toMatch(/warmup/i)
    expect(r.detail).toMatch(/synthetic/i)
    // The distinction is the point: warmup mail never reaches a campaign contact.
    expect(r.detail).not.toMatch(/real recipients/i)
  })

  // Pre-split rows genuinely do not know. Attributing one would invent exactly the
  // trust the split exists to establish.
  it('says the population is unrecorded rather than guessing', () => {
    for (const value of [null, undefined]) {
      const r = bounceRow({ bounce_population: value as never, bounce_rate: 0.12, bounce_samples: 200 })
      expect(r.label).toBe('Hard bounces')
      expect(r.detail).toMatch(/not recorded|predates/i)
      expect(r.detail).not.toMatch(/real recipients|synthetic/i)
    }
  })

  // An arm a newer server knows about and this build does not must not be folded
  // into either named population.
  it('does not fold an unknown population into campaign or warmup', () => {
    const r = bounceRow({ bounce_population: 'relay' as never, bounce_rate: 0.12, bounce_samples: 200 })
    expect(r.label).toMatch(/relay/i)
    expect(r.detail).not.toMatch(/real recipients|synthetic/i)
  })

  // The population label must not cost the lower-bound treatment: a rate of 0 over
  // a real sample is a floor, not a measurement, and must never read as a clean 0%.
  it('keeps the lower-bounded zero honest under a named population', () => {
    const r = bounceRow({ bounce_population: 'campaign', bounce_rate: 0, bounce_samples: 5 })
    expect(r.value).toMatch(/not established/i)
    expect(r.value).not.toMatch(/\b0(\.0+)?\s*%/)
  })
})

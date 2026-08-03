import { describe, expect, test } from 'vitest'
import type { CampaignGuardrails, CampaignPauseEvent, DeliverabilityScore, ScoreComponent } from '@/store/api'
import {
  MAX_THRESHOLD_PCT,
  MIN_THRESHOLD_PCT,
  autoPauseCopy,
  componentCopies,
  componentCopy,
  formatPct,
  guardrailsErrorMessage,
  pauseEventSentence,
  pauseReasonLabel,
  reportErrorMessage,
  scoreHeadline,
  shortDate,
  thresholdFromDraft,
  unmeasuredComponents,
  verdictCopy,
} from './deliverability-copy'

const NOW = Date.parse('2026-08-20T12:00:00Z')

function component(overrides: Partial<ScoreComponent> = {}): ScoreComponent {
  return { key: 'bounce', label: 'Bounces', penalty: 0, rate: 0, measured: true, ...overrides }
}

function score(overrides: Partial<DeliverabilityScore> = {}): DeliverabilityScore {
  return { value: 92, confidence: 'high', delivered: 4_120, components: [component()], ...overrides }
}

const GUARDRAILS: CampaignGuardrails = {
  auto_pause_enabled: true,
  bounce_pause_pct: 8,
  complaint_pause_pct: 1.5,
}

describe('formatPct', () => {
  test('keeps two decimals below 1% so a real complaint rate never rounds to 0.0%', () => {
    expect(formatPct(0.3)).toBe('0.30%')
    expect(formatPct(0.04)).toBe('0.04%')
  })

  test('one decimal at 1% and above', () => {
    expect(formatPct(9.24)).toBe('9.2%')
    expect(formatPct(100)).toBe('100.0%')
  })

  test('an exact zero stays a single decimal, not 0.00%', () => {
    expect(formatPct(0)).toBe('0.0%')
  })
})

describe('shortDate', () => {
  test('day and short month within the current year', () => {
    expect(shortDate('2026-08-12T09:30:00Z', NOW)).toBe('12 Aug')
  })

  test('adds the year once it differs, so an old pause is not read as recent', () => {
    expect(shortDate('2025-08-12T09:30:00Z', NOW)).toBe('12 Aug 2025')
  })

  test('an unparseable timestamp does not render as a real date', () => {
    expect(shortDate('not-a-date', NOW)).toBe('an unknown date')
  })
})

describe('scoreHeadline', () => {
  test('low confidence replaces the band with "Provisional" and never a healthy tone', () => {
    const headline = scoreHeadline(score({ value: 96, confidence: 'low', delivered: 11 }))
    expect(headline.label).toBe('Provisional')
    expect(headline.provisional).toBe(true)
    expect(headline.tone).not.toBe('running')
    expect(headline.tone).toBe('draft')
    // The number is still shown — it is qualified, not hidden.
    expect(headline.value).toBe(96)
    expect(headline.qualifier).toContain('11 delivered')
    expect(headline.qualifier).toContain('too small a sample to be a verdict')
  })

  test('a high-confidence strong score reads as strong and states its sample', () => {
    const headline = scoreHeadline(score({ value: 92, confidence: 'high', delivered: 4120 }))
    expect(headline.label).toBe('Strong')
    expect(headline.tone).toBe('running')
    expect(headline.provisional).toBe(false)
    expect(headline.qualifier).toContain('4,120 delivered')
  })

  test('medium confidence says indicative rather than firm', () => {
    const headline = scoreHeadline(score({ confidence: 'medium', delivered: 90 }))
    expect(headline.provisional).toBe(false)
    expect(headline.qualifier).toContain('indicative')
  })

  test('the bands are labelled and toned distinctly', () => {
    expect(scoreHeadline(score({ value: 79 })).label).toBe('Watch')
    expect(scoreHeadline(score({ value: 79 })).tone).toBe('paused')
    expect(scoreHeadline(score({ value: 41 })).label).toBe('At risk')
    expect(scoreHeadline(score({ value: 41 })).tone).toBe('failing')
  })

  test('an unmeasured component is named as excluded from the number', () => {
    const headline = scoreHeadline(
      score({
        components: [component(), component({ key: 'complaint', label: 'Complaints', measured: false, rate: null })],
      }),
    )
    expect(headline.qualifier).toContain("Complaints wasn't measured")
    expect(headline.qualifier).toContain("didn't count toward it")
  })

  test('two unmeasured components are joined and pluralised', () => {
    const headline = scoreHeadline(
      score({
        components: [
          component({ key: 'complaint', label: 'Complaints', measured: false }),
          component({ key: 'spam_placement', label: 'Spam placement', measured: false }),
        ],
      }),
    )
    expect(headline.qualifier).toContain("Complaints and Spam placement weren't measured")
    expect(headline.qualifier).toContain("they didn't count")
  })
})

describe('componentCopy', () => {
  test('an unmeasured component reads as not measured, never as 0%, and is never healthy-toned', () => {
    const copy = componentCopy(component({ key: 'complaint', label: 'Complaints', measured: false, rate: null }))
    expect(copy.status).toBe('Not measured')
    expect(copy.status).not.toContain('%')
    expect(copy.tone).toBe('draft')
    expect(copy.tone).not.toBe('running')
    expect(copy.penaltyLabel).toBeNull()
    expect(copy.detail).toContain('No complaint feed is connected')
    expect(copy.detail).toContain('not a clean complaint rate')
  })

  test('an unmeasured component with a zero rate still reads as not measured', () => {
    // The dangerous case: a payload that carries `rate: 0` alongside
    // `measured: false`. The flag wins.
    const copy = componentCopy(component({ key: 'complaint', measured: false, rate: 0, penalty: 0 }))
    expect(copy.status).toBe('Not measured')
    expect(copy.measured).toBe(false)
  })

  test('a measured clean component shows its rate and the ok tone', () => {
    const copy = componentCopy(component({ rate: 0.42, penalty: 0 }))
    expect(copy.status).toBe('0.42% — clean')
    expect(copy.tone).toBe('running')
    expect(copy.penaltyLabel).toBeNull()
  })

  test('a measured component costing points names the cost, amber below a quarter', () => {
    const copy = componentCopy(component({ rate: 4.1, penalty: 16 }))
    expect(copy.status).toBe('4.1% — costing 16 points')
    expect(copy.penaltyLabel).toBe('−16 points')
    expect(copy.tone).toBe('paused')
  })

  test('a heavy penalty reads as a failure', () => {
    expect(componentCopy(component({ rate: 12, penalty: 40 })).tone).toBe('failing')
  })

  test('a rate-less component (warmup) uses the point cost and the server detail', () => {
    const copy = componentCopy(
      component({ key: 'warmup', label: 'Warmup', rate: null, penalty: 25, detail: 'Two mailboxes are throttled.' }),
    )
    expect(copy.status).toBe('Costing 25 points')
    expect(copy.detail).toBe('Two mailboxes are throttled.')
  })

  test('an empty server label falls back to a local name rather than rendering blank', () => {
    expect(componentCopy(component({ key: 'domain_auth', label: '   ' })).label).toBe('Domain authentication')
  })

  test('componentCopies and unmeasuredComponents keep contract order', () => {
    const s = score({
      components: [component(), component({ key: 'complaint', measured: false }), component({ key: 'warmup' })],
    })
    expect(componentCopies(s).map((c) => c.key)).toEqual(['bounce', 'complaint', 'warmup'])
    expect(unmeasuredComponents(s).map((c) => c.key)).toEqual(['complaint'])
  })
})

describe('verdictCopy', () => {
  test('warn is distinct from ok and paused in label, tone and actionability', () => {
    const ok = verdictCopy('ok', GUARDRAILS)
    const warn = verdictCopy('warn', GUARDRAILS)
    const paused = verdictCopy('paused', GUARDRAILS)

    expect(new Set([ok.label, warn.label, paused.label]).size).toBe(3)
    expect(new Set([ok.tone, warn.tone, paused.tone]).size).toBe(3)
    expect(warn.actionable).toBe(true)
    expect(ok.actionable).toBe(false)
    expect(paused.actionable).toBe(false)
    expect(warn.detail).toContain('Nothing has stopped yet')
    expect(warn.detail).toContain('8.0% bounces or 1.5% complaints')
  })

  test('paused points at the recorded reasons rather than just saying paused', () => {
    const paused = verdictCopy('paused', GUARDRAILS)
    expect(paused.label).not.toBe('Paused')
    expect(paused.detail).toContain('recorded below')
  })
})

describe('autoPauseCopy', () => {
  test('enabled explains the minimum-sample guarantee', () => {
    const copy = autoPauseCopy(GUARDRAILS)
    expect(copy.label).toBe('Auto-pause on')
    expect(copy.tone).toBe('running')
    expect(copy.detail).toContain('never pauses on a handful of sends')
  })

  test('disabled is called out as unenforced, not as a neutral setting', () => {
    const copy = autoPauseCopy({ ...GUARDRAILS, auto_pause_enabled: false })
    expect(copy.label).toBe('Auto-pause off')
    expect(copy.tone).toBe('failing')
    expect(copy.detail).toContain('not enforced')
  })
})

describe('pauseEventSentence', () => {
  const event: CampaignPauseEvent = {
    reason: 'bounce_spike',
    metric: 'bounce_rate',
    value: 9.2,
    threshold: 8,
    delivered: 218,
    created_at: '2026-08-12T04:11:00Z',
  }

  test('carries reason, observed rate, threshold and sample in one sentence', () => {
    expect(pauseEventSentence(event, NOW)).toBe(
      'Paused automatically on 12 Aug — bounce rate 9.2% over 218 delivered, threshold 8.0%.',
    )
    expect(pauseReasonLabel(event)).toBe('Bounce spike')
  })

  test('a complaint pause names the complaint rate and keeps sub-1% precision', () => {
    const complaint: CampaignPauseEvent = {
      ...event,
      reason: 'complaint_spike',
      metric: 'complaint_rate',
      value: 1.62,
      threshold: 1.5,
      delivered: 900,
    }
    const sentence = pauseEventSentence(complaint, NOW)
    expect(sentence).toContain('complaint rate 1.6%')
    expect(sentence).toContain('over 900 delivered')
    expect(sentence).toContain('threshold 1.5%')
    expect(pauseReasonLabel(complaint)).toBe('Complaint spike')
  })
})

describe('thresholdFromDraft', () => {
  test('accepts a value inside the bounds', () => {
    expect(thresholdFromDraft('8', 'bounce')).toEqual({ pct: 8 })
    expect(thresholdFromDraft(' 1.5 ', 'complaint')).toEqual({ pct: 1.5 })
    expect(thresholdFromDraft(String(MIN_THRESHOLD_PCT), 'bounce')).toEqual({ pct: MIN_THRESHOLD_PCT })
    expect(thresholdFromDraft(String(MAX_THRESHOLD_PCT), 'bounce')).toEqual({ pct: MAX_THRESHOLD_PCT })
  })

  test('refuses 0 — it would pause every campaign the moment the sample is reached', () => {
    expect(thresholdFromDraft('0', 'bounce')).toEqual({
      problem: 'Bounce threshold must be between 0.1% and 100% — got 0%.',
    })
  })

  test('refuses above 100 and names the field', () => {
    expect(thresholdFromDraft('120', 'complaint')).toEqual({
      problem: 'Complaint threshold must be between 0.1% and 100% — got 120%.',
    })
  })

  test('refuses empty and non-numeric input', () => {
    expect(thresholdFromDraft('', 'bounce')).toHaveProperty('problem')
    expect(thresholdFromDraft('eight', 'bounce')).toEqual({
      problem: 'Bounce threshold must be a number, e.g. 8 for 8%.',
    })
  })
})

describe('error copy', () => {
  test('a failed report says no score is shown rather than implying a clean result', () => {
    const message = reportErrorMessage({ status: 500, data: {} })
    expect(message).toContain('(500)')
    expect(message).toContain('not a clean result')
  })

  test('403 is about access, not about deliverability', () => {
    expect(reportErrorMessage({ status: 403, data: {} })).toContain("don't have access")
  })

  test('a transport error still reads as a failed request', () => {
    expect(reportErrorMessage({ status: 'FETCH_ERROR', error: 'offline' })).toContain('failed request')
  })

  test('a rejected save says the previous settings still apply', () => {
    expect(guardrailsErrorMessage({ status: 422, data: {} })).toContain('between 0.1% and 100%')
    expect(guardrailsErrorMessage({ status: 404, data: {} })).toContain('no longer exists')
    expect(guardrailsErrorMessage({ status: 500, data: { error: 'db down' } })).toContain(
      'db down. The previous settings are still in force',
    )
    expect(guardrailsErrorMessage(undefined)).toContain('previous settings are still in force')
  })
})

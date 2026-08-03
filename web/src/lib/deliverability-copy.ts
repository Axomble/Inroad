// The score → copy mapping for the deliverability surfaces, kept out of JSX
// because the wording *is* the feature: an operator decides whether to keep
// sending based on these sentences, so a number presented more confidently than
// the evidence supports is the failure mode this module exists to prevent.
//
// Lives in `lib/` rather than a feature because two features render it — the
// Deliverability page (workspace rollup) and the campaign detail's guardrails
// card — and features may not import each other (same reasoning as
// `lib/warmup-health.ts`).
//
// Four judgements from the API contract's own field descriptions are encoded
// here rather than left to whoever next touches the markup:
//
//  1. `measured: false` means ABSENT, not zero. A component nobody measured must
//     never render as "0.0%" and must never wear a healthy colour — that would
//     tell an operator their complaint rate is clean when no feed was ever read.
//  2. `confidence: 'low'` disqualifies the headline number rather than annotating
//     it. 96 over eleven delivered emails is not a clean bill of health, so at low
//     confidence the score reads as provisional and is never coloured as good.
//  3. A `warn` verdict is its own state, distinct from both `ok` and `paused`: it
//     is the last moment an operator can act, so its copy says what to do.
//  4. A pause always states reason, observed rate, threshold and sample. A stopped
//     campaign with no explanation is worse than no breaker at all.
import type { StatusTone } from '@/components/shared/status-pill'
import { httpStatus, isFetchBaseQueryError } from '@/lib/rtk-error'
import type {
  CampaignDeliverability,
  CampaignGuardrails,
  CampaignPauseEvent,
  DeliverabilityScore,
  ScoreComponent,
} from '@/store/api'

/* ------------------------------------------------------------------ numbers */

/**
 * A rate the API gives as a percentage (9.2 meaning 9.2%). Sub-1% rates keep two
 * decimals: complaint tolerances live at 0.30%, so one decimal would round a
 * real signal to "0.0%" — the exact false-clean reading this module avoids.
 */
export function formatPct(rate: number): string {
  const decimals = rate !== 0 && Math.abs(rate) < 1 ? 2 : 1
  return `${rate.toFixed(decimals)}%`
}

/** "218 delivered" / "1 delivered", for the sample a number was judged on. */
export function deliveredLabel(delivered: number): string {
  return `${delivered.toLocaleString()} delivered`
}

/**
 * "12 Aug", or "12 Aug 2025" once the year differs from the reader's — a pause
 * from a previous year read as if it happened last week otherwise. `now` is
 * injectable so tests are deterministic.
 */
export function shortDate(iso: string, now: number = Date.now()): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return 'an unknown date'
  const sameYear = at.getUTCFullYear() === new Date(now).getUTCFullYear()
  return at.toLocaleDateString('en-GB', {
    day: 'numeric',
    month: 'short',
    ...(sameYear ? {} : { year: 'numeric' }),
    timeZone: 'UTC',
  })
}

/* ------------------------------------------------------------- the headline */

export interface ScoreHeadline {
  /** The number itself. Always rendered — this is the page's hero figure. */
  value: number
  /** Two or three words beside the number; never colour alone. */
  label: string
  tone: StatusTone
  /**
   * A full sentence qualifying the number: the sample it came from, and what it
   * excludes. Always present, because there is no sample size at which "96" on
   * its own is honest.
   */
  qualifier: string
  /**
   * True when the sample is too small for the score to be read as a verdict. The
   * headline is deliberately not coloured as healthy in this case.
   */
  provisional: boolean
}

/**
 * Band labels, only reachable at medium/high confidence. A low-confidence score
 * never lands in a band at all — see `scoreHeadline`.
 */
function bandLabel(value: number): { label: string; tone: StatusTone } {
  if (value >= 80) return { label: 'Strong', tone: 'running' }
  if (value >= 60) return { label: 'Watch', tone: 'paused' }
  return { label: 'At risk', tone: 'failing' }
}

/** The components the API reported as not measured, in contract order. */
export function unmeasuredComponents(score: DeliverabilityScore): ScoreComponent[] {
  return score.components.filter((c) => !c.measured)
}

/** "Complaints" / "Complaints and spam placement" — for the exclusion sentence. */
function joinLabels(components: ScoreComponent[]): string {
  const labels = components.map((c) => componentName(c))
  if (labels.length <= 1) return labels[0] ?? ''
  const last = labels[labels.length - 1] ?? ''
  return `${labels.slice(0, -1).join(', ')} and ${last}`
}

/**
 * The headline number and the sentence that qualifies it.
 *
 * Low confidence does not get a badge next to a green 96 — it replaces the band
 * entirely with "Provisional" and a faint tone, because a score computed over a
 * handful of sends is not evidence of health and must not be read as any.
 */
export function scoreHeadline(score: DeliverabilityScore): ScoreHeadline {
  const missing = unmeasuredComponents(score)
  const exclusion = missing.length
    ? ` ${joinLabels(missing)} ${missing.length === 1 ? "wasn't" : "weren't"} measured, so ${
        missing.length === 1 ? 'it' : 'they'
      } didn't count toward it.`
    : ''

  if (score.confidence === 'low') {
    return {
      value: score.value,
      label: 'Provisional',
      // Deliberately not `running`: a faint tone reads as "no verdict yet",
      // which is exactly what a score over too small a sample is.
      tone: 'draft',
      provisional: true,
      qualifier:
        `Computed over ${deliveredLabel(score.delivered)} — too small a sample to be a verdict, ` +
        `so read this as provisional rather than as a score.${exclusion}`,
    }
  }

  const band = bandLabel(score.value)
  const strength =
    score.confidence === 'medium'
      ? `Computed over ${deliveredLabel(score.delivered)} — enough to be indicative, not yet enough to be firm.`
      : `Computed over ${deliveredLabel(score.delivered)}.`
  return {
    value: score.value,
    label: band.label,
    tone: band.tone,
    provisional: false,
    qualifier: `${strength}${exclusion}`,
  }
}

/* ------------------------------------------------------- score  components */

const COMPONENT_NAMES: Record<ScoreComponent['key'], string> = {
  bounce: 'Bounces',
  complaint: 'Complaints',
  spam_placement: 'Spam placement',
  warmup: 'Warmup',
  domain_auth: 'Domain authentication',
}

/** The component's own label, falling back to a local name for an empty one. */
export function componentName(component: ScoreComponent): string {
  const trimmed = component.label.trim()
  return trimmed.length > 0 ? trimmed : COMPONENT_NAMES[component.key]
}

/**
 * Why each signal is absent, in terms of what an operator would do about it. The
 * complaint case is spelled out because it is the one that ships unmeasured: v1
 * has no inbound complaint feed, so this copy is what stops the panel from
 * implying a clean complaint rate.
 */
const NOT_MEASURED_DETAIL: Record<ScoreComponent['key'], string> = {
  complaint:
    'No complaint feed is connected, so this was never looked at — it is not a clean complaint rate. Point a provider feed (an SES SNS subscriber, say) at the deliverability ingest endpoint to start measuring it.',
  spam_placement:
    'No warmup receipts landed in the window, so placement was never observed. Enable warmup on your sending mailboxes to measure where your mail actually lands.',
  bounce: 'Nothing has been delivered in this window, so there is no bounce rate to compute yet.',
  warmup: 'None of these mailboxes is warming up, so there is no warmup state to read.',
  domain_auth: 'No SPF/DMARC lookup has answered for these domains yet, so authentication is unknown.',
}

export interface ComponentCopy {
  key: ScoreComponent['key']
  /** Always rendered — colour is never the only signal. */
  label: string
  /** Two or three words: "0.42% — clean" / "Not measured". */
  status: string
  detail: string
  measured: boolean
  /**
   * `draft` (faint) whenever a component wasn't measured — never `running`.
   * A green "not measured" would read as a pass.
   */
  tone: StatusTone
  /** "−12 points", or null when nothing was subtracted or nothing was measured. */
  penaltyLabel: string | null
}

/**
 * One component's row copy. Three distinct shapes, and the distinction between
 * the first two is the whole point: not measured, measured and clean, measured
 * and costing points.
 */
export function componentCopy(component: ScoreComponent): ComponentCopy {
  const label = componentName(component)
  const serverDetail = component.detail?.trim()

  if (!component.measured) {
    return {
      key: component.key,
      label,
      status: 'Not measured',
      detail: serverDetail && serverDetail.length > 0 ? serverDetail : NOT_MEASURED_DETAIL[component.key],
      measured: false,
      tone: 'draft',
      penaltyLabel: null,
    }
  }

  const rate = typeof component.rate === 'number' ? component.rate : null
  const penalty = Math.max(0, Math.round(component.penalty))

  if (penalty === 0) {
    return {
      key: component.key,
      label,
      status: rate === null ? 'No penalty' : `${formatPct(rate)} — clean`,
      detail: serverDetail && serverDetail.length > 0 ? serverDetail : 'Measured, and nothing was subtracted for it.',
      measured: true,
      tone: 'running',
      penaltyLabel: null,
    }
  }

  return {
    key: component.key,
    label,
    status: rate === null ? `Costing ${penalty} points` : `${formatPct(rate)} — costing ${penalty} points`,
    detail:
      serverDetail && serverDetail.length > 0
        ? serverDetail
        : `This signal subtracted ${penalty} points from the score.`,
    measured: true,
    // A quarter of the score or more is a real problem; below that it is a
    // trend worth watching, not a fault.
    tone: penalty >= 25 ? 'failing' : 'paused',
    penaltyLabel: `−${penalty} points`,
  }
}

export function componentCopies(score: DeliverabilityScore): ComponentCopy[] {
  return score.components.map(componentCopy)
}

/* ------------------------------------------------------- campaign guardrails */

export type Verdict = CampaignDeliverability['verdict']

export interface VerdictCopy {
  label: string
  tone: StatusTone
  detail: string
  /** True for `warn` only: the state where the operator can still act. */
  actionable: boolean
}

/**
 * The campaign's current verdict. `warn` is deliberately its own label, tone and
 * sentence — flattening it into either neighbour destroys the only state that
 * gives an operator time to react.
 */
export function verdictCopy(verdict: Verdict, guardrails: CampaignGuardrails): VerdictCopy {
  const limits = `${formatPct(guardrails.bounce_pause_pct)} bounces or ${formatPct(
    guardrails.complaint_pause_pct,
  )} complaints`

  if (verdict === 'paused') {
    return {
      label: 'Paused by the guardrail',
      tone: 'failing',
      actionable: false,
      detail:
        'The breaker stopped this campaign. Every pause is recorded below with the rate it saw and the sample it judged — fix the underlying list or copy, then restart the campaign the normal way.',
    }
  }
  if (verdict === 'warn') {
    return {
      label: 'Trending toward a pause',
      tone: 'paused',
      actionable: true,
      detail: `A rate has entered the warning band below its pause threshold. Nothing has stopped yet — this is the point where cleaning the list or fixing the copy still prevents a pause at ${limits}.`,
    }
  }
  return {
    label: 'Within limits',
    tone: 'running',
    actionable: false,
    detail: `Both measured rates sit below their thresholds. The breaker fires at ${limits}.`,
  }
}

/**
 * One sentence per automatic pause, carrying reason, observed rate, threshold and
 * sample: *"Paused automatically on 12 Aug — bounce rate 9.2% over 218 delivered,
 * threshold 8%."* A bare "paused" is the shape of this feature that strands an
 * operator, so every field the API records is spent here.
 */
export function pauseEventSentence(event: CampaignPauseEvent, now: number = Date.now()): string {
  const metric = event.metric === 'complaint_rate' ? 'complaint rate' : 'bounce rate'
  return (
    `Paused automatically on ${shortDate(event.created_at, now)} — ` +
    `${metric} ${formatPct(event.value)} over ${deliveredLabel(event.delivered)}, ` +
    `threshold ${formatPct(event.threshold)}.`
  )
}

/** What tripped it, as a short label beside the sentence. */
export function pauseReasonLabel(event: CampaignPauseEvent): string {
  return event.reason === 'complaint_spike' ? 'Complaint spike' : 'Bounce spike'
}

/**
 * The on/off state of the breaker itself. Disabled is called out because the
 * verdict copy above then describes a limit nothing enforces.
 */
export function autoPauseCopy(guardrails: CampaignGuardrails): { label: string; tone: StatusTone; detail: string } {
  return guardrails.auto_pause_enabled
    ? {
        label: 'Auto-pause on',
        tone: 'running',
        detail:
          'This campaign stops itself if a rate crosses its threshold on a large enough sample. It never pauses on a handful of sends.',
      }
    : {
        label: 'Auto-pause off',
        tone: 'failing',
        detail:
          'Nothing will stop this campaign automatically. The thresholds below are recorded but not enforced, so a bad list keeps sending until someone notices.',
      }
}

/* ------------------------------------------------- threshold field validation */

/**
 * The contract's bounds on both threshold percentages. 0 is excluded on purpose:
 * a 0% threshold would pause every campaign the moment the minimum sample is
 * reached, which is a footgun rather than a strict setting.
 */
export const MIN_THRESHOLD_PCT = 0.1
export const MAX_THRESHOLD_PCT = 100

export type ThresholdField = 'bounce' | 'complaint'

const THRESHOLD_NAME: Record<ThresholdField, string> = {
  bounce: 'Bounce threshold',
  complaint: 'Complaint threshold',
}

/** A threshold as the form holds it: a raw string, so a half-typed "0." stays invalid. */
export function thresholdToDraft(pct: number): string {
  return String(pct)
}

/**
 * Parses one threshold field, refusing anything outside 0.1–100 here so the
 * operator gets the specific reason instead of the API's 422 — and so no request
 * is sent for a value we already know is invalid.
 */
export function thresholdFromDraft(
  raw: string,
  field: ThresholdField,
): { pct: number } | { problem: string } {
  const trimmed = raw.trim()
  const name = THRESHOLD_NAME[field]
  if (trimmed === '') return { problem: `${name} can't be empty — it must be between ${MIN_THRESHOLD_PCT}% and ${MAX_THRESHOLD_PCT}%.` }
  const value = Number(trimmed)
  if (!Number.isFinite(value)) return { problem: `${name} must be a number, e.g. 8 for 8%.` }
  if (value < MIN_THRESHOLD_PCT || value > MAX_THRESHOLD_PCT) {
    return { problem: `${name} must be between ${MIN_THRESHOLD_PCT}% and ${MAX_THRESHOLD_PCT}% — got ${trimmed}%.` }
  }
  return { pct: value }
}

/* --------------------------------------------------------------- error copy */

/** The `{"error": "…"}` envelope the API writes, read through the typed seam. */
function serverReason(error: unknown): string | undefined {
  if (!isFetchBaseQueryError(error)) return undefined
  const reason = (error.data as { error?: string } | undefined)?.error
  return typeof reason === 'string' && reason.length > 0 ? reason : undefined
}

/**
 * Copy for a failed report load. Says plainly that nothing is known, because the
 * alternative reading — an empty dashboard as "all clear" — is the dangerous one.
 */
export function reportErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 403) return "You don't have access to this workspace's deliverability data."
  return `Couldn't load deliverability${status ? ` (${status})` : ''}. No score is being shown — this is a failed request, not a clean result.`
}

/** Copy for a failed guardrails save. */
export function guardrailsErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 404) return 'This campaign no longer exists.'
  if (status === 422) {
    return `Those thresholds were rejected — both must be between ${MIN_THRESHOLD_PCT}% and ${MAX_THRESHOLD_PCT}%.`
  }
  const reason = serverReason(error)
  return reason
    ? `Couldn't save the guardrails: ${reason}. The previous settings are still in force.`
    : "Couldn't save the guardrails. The previous settings are still in force — try again."
}

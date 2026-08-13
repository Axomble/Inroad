// The words the warmup transition history is made of.
//
// Kept out of JSX because the wording *is* the feature. The reputation design's
// standing objection is that an operator must never be shown a bare score, and
// three of the numbers on a transition row are the exact shape of number that
// misleads: `spam_rate`, `bounce_rate` and `complaint_rate` are confidence-
// adjusted 95% LOWER BOUNDS, and they are reported as 0 when the sample did not
// meet the policy minimum. A mailbox with 3 spam placements in 5 sends therefore
// arrives here as `spam_rate: 0` — printing "0% spam" beside it would tell an
// operator the opposite of the truth. Every rate in this module is rendered as a
// bound with its sample size, or as words saying it was never established.
//
// Reason codes get the same treatment `healthMeta`/`laneMeta` give states: a
// stable machine token maps to a sentence written for an operator. The map is the
// primary copy and the server's own `reason` prose is the fallback for a code
// this build has not learned yet — so a newer policy degrades to the engine's
// words rather than leaking `spam_pause` into the UI.
import type { WarmupTransition } from '@/store/api'
import { toWarmupHealth, type WarmupHealth } from '@/lib/warmup-health'
import { toWarmupLane, type WarmupLane } from '@/lib/warmup-lane'

/* ------------------------------------------------------------------- reasons */

/**
 * Health-axis reason codes, from `internal/platform/warmup/policy.go`. Each band
 * says what crossed and what the crossing cost, because "watch" and "pause" are
 * different instructions to the reader, not different shades of the same one.
 */
const HEALTH_REASONS: Record<string, string> = {
  spam_watch: 'Spam placement crossed the watch threshold',
  spam_throttle: 'Spam placement crossed the throttle threshold, so daily volume was cut',
  spam_pause: 'Spam placement crossed the pause threshold, so sending stopped',
  campaign_bounce_watch: 'Campaign hard bounces crossed the watch threshold',
  campaign_bounce_throttle: 'Campaign hard bounces crossed the throttle threshold, so daily volume was cut',
  campaign_bounce_pause: 'Campaign hard bounces crossed the pause threshold, so sending stopped',
  warmup_bounce_watch: 'Warmup hard bounces crossed the watch threshold',
  warmup_bounce_throttle: 'Warmup hard bounces crossed the throttle threshold, so daily volume was cut',
  warmup_bounce_pause: 'Warmup hard bounces crossed the pause threshold, so sending stopped',
  complaint_watch: 'Complaints crossed the watch threshold',
  complaint_throttle: 'Complaints crossed the throttle threshold, so daily volume was cut',
  complaint_pause: 'Complaints crossed the pause threshold, so sending stopped',
  placement_sample_insufficient: 'Not enough fresh placement evidence to judge this mailbox — unproven, not clean',
  insufficient_evidence_to_recover: 'Held where it was: not enough fresh placement evidence to recover',
  recovery_step: 'A clean qualified window, so it recovered one step',
  recovery_blocked_by_dwell: 'Recovery is held back until the current pause has served its dwell time',
  evidence_qualified: 'Qualified placement evidence established this mailbox as healthy',
  health_unchanged: 'Reputation did not change; this entry moves the pool lane',
}

/**
 * Lane-axis reason codes. Every one of these names the condition that clears it
 * where such a condition exists — the phase-1 design is explicit that a withheld
 * mailbox must be told what gets it back, never just that it is withheld.
 */
const LANE_REASONS: Record<string, string> = {
  lane_pending_auth: 'Waiting on domain authentication — no warmup mail and no new campaign leads until it passes',
  lane_auth_regressed: 'Domain authentication stopped passing, so the mailbox went back to awaiting DNS',
  lane_admitted_to_probation: 'Domain authentication passed, so it was admitted to probation to prove itself at a floor volume',
  lane_quarantined: 'Withheld from the pool — no warmup mail and no new campaign leads until fresh qualifying evidence clears it',
  lane_quarantine_held: 'Still withheld: time alone never clears a quarantine — it needs fresh qualifying evidence and passing domain authentication',
  lane_quarantine_resumed: 'Domain authentication passed, but the quarantine cooldown has not elapsed yet',
  lane_cooldown_active: 'The quarantine cooldown is still running',
  lane_cooldown_elapsed: 'Cooldown elapsed, so it moved to recovery to earn its way back at a floor volume',
  lane_qualified: 'A qualified clean window, so it was promoted to the healthy pool',
  lane_awaiting_evidence: 'Waiting for a qualified clean window before it can be promoted',
  lane_recovered: 'Clean qualified evidence, so it returned to the healthy pool',
  lane_watch: 'Moved to watch — reduced volume while the signal is diagnosed',
  lane_watch_held: 'Held on watch until clean evidence arrives',
  lane_evidence_lapsed: 'No fresh qualified evidence, so it went back to probation',
  lane_healthy: 'Clean qualified evidence — full participation in the pool',
  lane_blocked_held: 'Blocked — an operator has to approve re-entry before recovery can start',
}

/** `spam_incinerated` → `Spam incinerated`. Last resort; never shows the token as-is. */
function humanizeCode(code: string): string {
  const words = code.trim().replaceAll('_', ' ')
  return words.length === 0 ? '' : `${words.charAt(0).toUpperCase()}${words.slice(1)}`
}

function lookup(map: Record<string, string>, code: string, reason: string): string {
  const mapped = map[code.trim()]
  if (mapped) return mapped
  const prose = reason.trim()
  if (prose) return prose
  return humanizeCode(code)
}

/**
 * Why the health axis landed where it did, as a sentence. Falls back to the
 * server's prose, then to a humanised code — the raw `reason_code` never reaches
 * the screen.
 */
export function reasonCopy(code: string, reason: string): string {
  return lookup(HEALTH_REASONS, code, reason) || 'No reason was recorded for this change.'
}

/**
 * The lane axis's own explanation, or `null` when the row carries none — rows
 * written before pool lanes existed, and rows that moved only the health axis.
 * That is an absence to render as absence, not empty copy.
 */
export function laneReasonCopy(
  code: string | null | undefined,
  reason: string | null | undefined,
): string | null {
  const trimmedCode = code?.trim() ?? ''
  const trimmedReason = reason?.trim() ?? ''
  if (!trimmedCode && !trimmedReason) return null
  return lookup(LANE_REASONS, trimmedCode, trimmedReason)
}

/* --------------------------------------------------------------- the two axes */

/**
 * A row can move the reputation axis, the pool axis, or both, so each axis
 * reports its own movement and neither is collapsed into the other.
 */
export type HealthChange =
  | { kind: 'moved'; from: WarmupHealth; to: WarmupHealth }
  | { kind: 'unchanged'; state: WarmupHealth }

export type LaneChange =
  | { kind: 'moved'; from: WarmupLane; to: WarmupLane }
  | { kind: 'unchanged'; lane: WarmupLane }
  /** Written before pool lanes existed: history without a lane, not a failure. */
  | { kind: 'unrecorded' }

export function healthChange(transition: WarmupTransition): HealthChange {
  const from = toWarmupHealth(transition.from_state)
  const to = toWarmupHealth(transition.to_state)
  return from === to ? { kind: 'unchanged', state: to } : { kind: 'moved', from, to }
}

export function laneChange(transition: WarmupTransition): LaneChange {
  const from = transition.from_lane ?? null
  const to = transition.to_lane ?? null
  // Absent on BOTH ends is the pre-lane row. A value that is merely unreadable
  // (a lane from a newer policy) is present, so it still narrows — to the
  // unproven lane, never to the healthy pool.
  if (from === null && to === null) return { kind: 'unrecorded' }
  const fromLane = toWarmupLane(from)
  const toLane = toWarmupLane(to)
  return fromLane === toLane
    ? { kind: 'unchanged', lane: toLane }
    : { kind: 'moved', from: fromLane, to: toLane }
}

/* ---------------------------------------------------------------- the evidence */

export interface EvidenceRow {
  /** Always rendered: a rate with no name is as unreadable as one with no sample. */
  label: string
  /** Words, or a figure carrying its direction — never a bare percentage. */
  value: string
  /** The sentence that keeps `value` honest. */
  detail: string
  /** False whenever `value` must not be read as a measured rate. */
  proven: boolean
  /** The sample the value was judged on; `null` for a count with no denominator. */
  samples: number | null
}

function samplesText(samples: number): string {
  return `${samples.toLocaleString()} observation${samples === 1 ? '' : 's'}`
}

/**
 * A 0..1 fraction as a percentage, with enough precision for the threshold it is
 * judged against. Complaint bands live at 0.03%–0.3%, so one decimal place would
 * round a real signal to "0.0%" — the false-clean reading this module exists to
 * prevent.
 */
export function formatBoundedRate(rate: number): string {
  const pct = rate * 100
  if (pct >= 1) return `${pct.toFixed(1)}%`
  if (pct >= 0.01) return `${pct.toFixed(2)}%`
  return `${Number(pct.toPrecision(1))}%`
}

/**
 * One rate as an operator may safely read it.
 *
 * Three outcomes, and the distinction between the first two is the whole point:
 * nothing observed, observed but never established, and a genuine lower bound.
 * A rate of 0 over a real sample lands in the middle case — it means the sample
 * did not meet the policy's minimum, not that the mailbox was clean.
 */
function rateRow(label: string, subject: string, rate: number, samples: number): EvidenceRow {
  if (samples <= 0) {
    return {
      label,
      value: 'No observations',
      detail: `No ${subject} was measured in this window. An unmeasured rate is not a clean one.`,
      proven: false,
      samples,
    }
  }
  if (rate <= 0) {
    return {
      label,
      value: 'Not established',
      detail: `Reported as 0 over ${samplesText(
        samples,
      )} — a floor, not a measurement: a sample below the policy minimum reports 0 whatever was observed.`,
      proven: false,
      samples,
    }
  }
  return {
    label,
    value: `at least ${formatBoundedRate(rate)}`,
    detail: `A 95% confidence lower bound over ${samplesText(
      samples,
    )}; the real rate is at least this and may be higher.`,
    proven: true,
    samples,
  }
}

/**
 * The evidence behind one transition, in the order an operator triages it. The
 * forged-token count is appended only when non-zero, and is labelled as the
 * observer-side signal it is: it describes mail this mailbox RECEIVED and never
 * counts against it as a sender.
 */
export function evidenceRows(transition: WarmupTransition): EvidenceRow[] {
  const rows: EvidenceRow[] = [
    rateRow('Spam placement', 'placement', transition.spam_rate, transition.placement_samples),
    rateRow('Hard bounces', 'delivery', transition.bounce_rate, transition.bounce_samples),
    rateRow('Complaints', 'delivery', transition.complaint_rate, transition.complaint_samples),
  ]
  if (transition.invalid_tokens > 0) {
    rows.push({
      label: 'Forged tokens received',
      value: String(transition.invalid_tokens),
      detail:
        'Warmup tokens this mailbox received that failed verification. Observer-side only: an unauthenticated token can claim any sender, so this is never counted against this mailbox as a sender.',
      proven: true,
      samples: null,
    })
  }
  return rows
}

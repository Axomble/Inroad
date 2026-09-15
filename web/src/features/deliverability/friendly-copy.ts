// Founder-facing rewording for the workspace deliverability page, layered over
// `lib/deliverability-copy`. The lib module is shared with the campaign
// guardrails card (features may not import each other, so it lives in lib/) and
// keeps the operator-precise sentences; this page speaks to founders and
// marketers, so the two surfaces it owns — the provisional headline and the
// "nothing was measured" component rows — are re-worded here without touching
// the shared judgements (what counts as measured, tones, penalties).
import {
  componentCopy,
  scoreHeadline,
  type ComponentCopy,
  type ScoreHeadline,
} from '@/lib/deliverability-copy'
import type { DeliverabilityScore, ScoreComponent } from './api'

/**
 * Plain-language reasons a check has no data yet, keyed like the lib's map. Used
 * only when the API sent no detail of its own — a server-provided sentence still
 * wins, exactly as in the lib.
 */
const FRIENDLY_NOT_MEASURED: Record<ScoreComponent['key'], string> = {
  complaint:
    "Complaint reports aren't connected yet, so this doesn't mean your complaint rate is clean — nothing was ever counted. Connect your email provider's feedback feed to start measuring it.",
  spam_placement:
    "No warmup emails landed in this period, so we couldn't see where your mail is landing. Turn on warmup for your mailboxes to start measuring this.",
  bounce: "Nothing has been sent in this period, so there's no bounce rate to show yet.",
  warmup: "None of these mailboxes is warming up yet, so there's nothing to measure here.",
  domain_auth:
    "Your domains haven't been checked yet, so we don't know how they're set up. Run a check from the Mailboxes page.",
}

/**
 * The headline, with the low-confidence qualifier said the way a founder would
 * hear it: not enough sent yet, and it firms up on its own. The honest sample
 * size stays in the sentence; the tone and figure treatment stay the lib's
 * (faint, never coloured as a verdict).
 */
export function friendlyScoreHeadline(score: DeliverabilityScore): ScoreHeadline {
  const headline = scoreHeadline(score)
  if (!headline.provisional) return headline
  return {
    ...headline,
    label: 'Early estimate',
    qualifier:
      `You haven't sent enough email yet for a reliable score — this is based on just ` +
      `${score.delivered.toLocaleString()} delivered so far and will firm up as you send more. ` +
      `The checks below start filling in as your mail goes out.`,
  }
}

/**
 * The component rows, with unmeasured ones reading "No data yet" plus a plain
 * reason. Measured rows pass through untouched, and a detail the server wrote
 * itself is kept verbatim (the lib already preferred it, so `copy.detail` is it).
 */
export function friendlyComponentCopies(score: DeliverabilityScore): ComponentCopy[] {
  return score.components.map((component) => {
    const copy = componentCopy(component)
    if (copy.measured) return copy
    const serverDetail = component.detail?.trim()
    return {
      ...copy,
      status: 'No data yet',
      detail: serverDetail && serverDetail.length > 0 ? copy.detail : FRIENDLY_NOT_MEASURED[copy.key],
    }
  })
}

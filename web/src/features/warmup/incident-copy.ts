// The words a correlated incident is made of.
//
// Kept out of JSX for the reason `route-copy.ts` and `identity-copy.ts` give: the
// wording IS the feature. This module is the extreme case of that, because the
// underlying fact is one an English sentence promotes almost by accident.
//
// What the data supports is: "these four mailboxes are degrading, they share a
// signing domain, and the rest of the pool is not degrading". What it does NOT
// support is "the signing domain is why". Every plausible-sounding rendering of
// the first sentence — "signing domain failure", "DKIM incident", a red badge
// reading "signing domain" — states the second, and an operator who reads it goes
// and changes a DNS record that was never implicated. So:
//
//   An incident is a CORRELATION. The dimension is where to look next, never the
//   answer. Two dimensions can carry one underlying problem from different
//   angles, and a small pool puts the same four mailboxes on one domain, one
//   route and one return path at once.
//
//   The ARITHMETIC is shown, not a verdict. A lift of 2.1 and a lift of 12 are
//   very different findings and a badge that says "incident" hides the
//   difference, so the inside count, the outside count and the lift are all on
//   screen for an operator who wants to disagree with the inference.
//
//   `unknown` is NOT a fault domain. Grouping degraded mailboxes on a value that
//   means "we never resolved this" correlates on our own ignorance, and it would
//   fire hardest on the pools carrying the least data. The backend excludes it;
//   if one arrives anyway it is discarded here rather than rendered as a domain.
//
//   NO incident is a real answer, not an empty state to apologise for — and the
//   several ways of having none are different answers. Nothing degraded at all,
//   one mailbox degrading alone, a pool too small for concentration to be
//   measurable, and a genuinely unattributed set of degradations are four
//   distinct facts, and the one an empty array means depends on the pool it
//   arrived with.
import type { WarmupMailbox, WarmupOverview } from '@/store/api'
import { destinationLabel } from './route-copy'

/** One incident exactly as the contract defines it — one source of truth. */
export type WarmupIncident = NonNullable<WarmupOverview['incidents']>[number]

/** The vocabulary the contract constrains a fault dimension to. */
export type IncidentDimension = WarmupIncident['dimension']

/**
 * One figure behind a correlation, with the label that makes it readable on its
 * own. Three of these per incident, and they are not decoration: they are the
 * whole reason this renders a row rather than a badge.
 */
export interface IncidentStat {
  /** What the figure counts. */
  label: string
  /** The figure itself — short, so it can be asserted and scanned. */
  value: string
  /**
   * The sentence that keeps `value` from being over-read.
   *
   * Null when the label and the figure already say everything true about it: a
   * count of degraded mailboxes over a stated cohort needs no gloss, and three
   * sentences per row on a pool with three incidents would bury the one that
   * matters.
   */
  detail: string | null
}

/** One detected correlation, and everything needed to render it honestly. */
export interface IncidentReading {
  /** Stable per row: the backend reports one incident per dimension and value. */
  key: string
  /** The dimension in an operator's language — never the contract token. */
  dimension: string
  /** What sharing this value means, and what it does not. */
  dimensionDetail: string
  /** The shared value, as it arrived. */
  value: string
  /** Inside, outside, concentration — in that order, always all three. */
  stats: IncidentStat[]
  /**
   * The degraded mailboxes, named. Emails where the pool knows them, ids where
   * it does not — an id is ugly and honest, where dropping the member would make
   * the list disagree with the count beside it.
   */
  members: string[]
}

export type IncidentsReading =
  /**
   * Nothing to report and nothing to say: no inference has been made over
   * anything. Either the server does not report incidents (a build predating
   * them) or the workspace has no participants at all. Both render as silence,
   * because "no shared cause found" would claim a search nobody ran.
   */
  | { kind: 'unreported' }
  /** Nothing is degrading, so there is nothing to correlate. A real answer. */
  | { kind: 'quiet'; message: string }
  /** Degradation, and no concentration in it — or not enough pool to tell. */
  | { kind: 'none-found'; message: string }
  | {
      kind: 'detected'
      incidents: IncidentReading[]
      /** Said plainly when the strongest few are shown and others are not. */
      truncated: string | null
    }

/* --------------------------------------------------------------- panel copy */

/**
 * What the panel says about itself, above the rows.
 *
 * The second half is the correlation-not-cause rule in the operator's own view,
 * and it is here rather than per row on purpose: repeated under four rows it
 * becomes chrome to skip, and said once above them it is read.
 */
export const INCIDENTS_INTRO =
  'Degradation that is concentrated in one thing several mailboxes have in common, recomputed from the pool each time this page loads. Each row says only that: these mailboxes share this value, and degradation is concentrated among them rather than spread across the pool. It does not say the shared value is why — two dimensions can carry one underlying problem from different angles, and a small pool can put the same mailboxes on one domain, one route and one return path at once. The counts are shown so you can disagree with the inference.'

/**
 * Design §7, and it needs two reasons where the identity panel and the route
 * matrix each needed one. Deliberately not either of their sentences: the route
 * matrix gates nothing because nobody has calibrated a normal per-route rate,
 * which is a condition that expires; the second reason here does not expire, and
 * recording the difference is the point of writing it out.
 */
export const INCIDENTS_GATES_NOTHING =
  "Reported for visibility only: no threshold, lane or promotion decision reads any of it. Two reasons, either sufficient alone. The concentration these rows are reported from, and the cohort sizes behind them, are guesses nobody has calibrated against real pools yet. And the destination dimension rests on where mail was delivered, which inside one workspace is steerable by whoever controls a mailbox domain's MX — tolerable for a row you can dismiss, not for a control that withholds sending."

/* ------------------------------------------------------------- the absences */


function participantCount(count: number): string {
  return `${count} participant${count === 1 ? '' : 's'}`
}

function mailboxCount(count: number): string {
  return `${count} mailbox${count === 1 ? '' : 'es'}`
}

/**
 * Nothing is degrading. Distinct from every other empty reading, and the
 * distinction is the point: an operator who reads "no shared cause found" over a
 * healthy pool has been told the search came back empty, when there was nothing
 * to search across.
 */
function quietMessage(participants: number): string {
  return `No degradation in the pool: ${participantCount(participants)}, and none is degrading on either axis — reputation or pool lane — so there is nothing to correlate. Not the same as looking across degraded mailboxes and finding nothing in common; here there is nothing to look across.`
}

/**
 * Degradation with no attribution, and which of these three sentences is true
 * depends on the pool rather than on the empty array, which is identical in all
 * three cases.
 */
function noneFoundMessage(degraded: number, participants: number, minPool: number): string {
  if (degraded === 1) {
    return 'One mailbox is degrading, and one mailbox on its own cannot correlate with anything — a shared pattern takes at least two. So nothing here is a shared-cause finding, and nothing here rules one out either.'
  }
  if (participants < minPool) {
    return `${mailboxCount(degraded)} are degrading, and a pool of ${participantCount(participants)} cannot show concentration at all: concentration is a comparison between the mailboxes sharing a value and the rest of the pool, and that comparison needs at least ${participantCount(minPool)} before it can be made at all. Nothing is ruled out here; there is not enough pool to look.`
  }
  return `${mailboxCount(degraded)} are degrading, and no destination, signing domain, return path or sender domain runs through them in a way that stands out from the rest of the pool. No shared cause found, which is an answer: work through them one at a time rather than looking for one thing behind them all.`
}

/**
 * The strongest few, and no more — one per fault dimension is the most an
 * operator can act on, and it is the shape §6's vacuity case takes: one bad
 * relay in a twenty-mailbox pool can legitimately report on the route, the
 * signing domain, the return path and the sender domain at once. Beyond that the
 * rows push the mailbox list itself below the fold, which is what an operator
 * came here for.
 *
 * Said out loud when it bites, because a silent cap is a lie about how much was
 * found.
 */
const MAX_SHOWN = 4

function truncationNote(hidden: number): string | null {
  if (hidden <= 0) return null
  return `${hidden} weaker correlation${hidden === 1 ? '' : 's'} ${hidden === 1 ? 'is' : 'are'} not shown. The most concentrated are above; the rest are further from being distinguishable from the pool as a whole.`
}

/* ------------------------------------------------------------- the arithmetic */

/**
 * Lift as an operator can read it: one decimal where the difference between 2.1
 * and 2.8 matters, whole numbers once it plainly does not.
 *
 * A non-finite figure is not printed. `NaN×` on a screen is a rendering bug read
 * as a finding, and the two counts beside it carry the row perfectly well
 * without it — so the absence gets words rather than a number.
 */
function formatLift(lift: number): string | null {
  if (!Number.isFinite(lift)) return null
  if (lift >= 10) return `${Math.round(lift)}×`
  return `${lift.toFixed(1)}×`
}

/**
 * Concentration close to 1 is no concentration at all, so a row only just above
 * it is a hint rather than a pattern. Presentational: nothing is filtered on
 * this, and the number is on screen either way.
 */
const MARGINAL_LIFT = 3

const LIFT_DETAIL =
  'How many times more degraded the mailboxes sharing this value are than the rest of the pool. 1× would be no concentration at all.'

const MARGINAL_LIFT_DETAIL = `${LIFT_DETAIL} This one is barely above that, so read it as a hint and check the two counts beside it — a handful of mailboxes can land this way by chance.`

function liftStat(lift: number): IncidentStat {
  const value = formatLift(lift)
  if (value == null) {
    return {
      label: 'Concentration',
      value: 'Not stated',
      detail:
        'No usable concentration figure arrived with this correlation, so the two counts beside it are the whole of the evidence. Not a zero, and not a strong result.',
    }
  }
  return {
    label: 'Concentration',
    value,
    detail: lift < MARGINAL_LIFT ? MARGINAL_LIFT_DETAIL : LIFT_DETAIL,
  }
}

/**
 * The two counts, which are the check on the inference. Both are needed and
 * neither means anything alone: 4 of 5 degraded is only a finding while the rest
 * of the pool is 1 of 20, and it is nothing at all while the rest is 18 of 20.
 */
function cohortStats(incident: WarmupIncident): IncidentStat[] {
  return [
    {
      label: 'Degraded, of those sharing it',
      value: `${incident.degraded_inside} of ${incident.cohort_size}`,
      detail: null,
    },
    {
      label: 'Degraded, of the rest of the pool',
      value: `${incident.degraded_outside} of ${incident.cohort_outside}`,
      detail: null,
    },
    liftStat(incident.lift),
  ]
}

/* ------------------------------------------------------------- the dimensions */

interface DimensionCopy {
  /** The row's name, in an operator's words. `signing_domain` never ships. */
  label: string
  /** What the shared value is, and what sharing it does not establish. */
  detail: string
}

/**
 * Keyed by the contract's own union, so a dimension added to `api/openapi.yaml`
 * fails to compile until it has copy — the guard `DESTINATION_COPY` and
 * `VERDICT_COPY` give their vocabularies.
 */
const DIMENSION_COPY: Record<IncidentDimension, DimensionCopy> = {
  destination_route: {
    label: 'destination',
    detail:
      "Their warmup mail was delivered to the same receiving provider. What these mailboxes have in common is where their mail went — decided by the recipient domain's MX, not by anything about how they send.",
  },
  signing_domain: {
    label: 'signing domain (DKIM)',
    detail:
      'Their last observed warmup mail was signed by the same DKIM d= domain. They share a signer; whether the signature has anything to do with the degradation is not something this can tell you.',
  },
  return_path_domain: {
    label: 'return path',
    detail:
      'Bounces for their mail go to the same host — the exact host, not folded to the organizational domain, so one bounce host degrading while its siblings are fine is visible here rather than averaged away.',
  },
  sender_domain: {
    label: 'sender domain',
    detail:
      'They send from the same organizational domain, so what is concentrated may be one domain rather than several mailboxes. Which of the two it is, this does not say.',
  },
}

/**
 * A dimension this build has no reading for. The backend's vocabulary is closed;
 * the JSON boundary is not.
 *
 * Shown as it arrived, for the reason an unrecognised destination and an
 * unrecognised verdict are: folding it into one of the four would attribute the
 * correlation to a dimension nobody reported.
 */
function unrecognisedDimension(raw: string): DimensionCopy {
  return {
    label: `${raw} — a dimension this build does not know`,
    detail: `These mailboxes share a value on "${raw}". This build has no reading for that dimension, so it is named as it arrived rather than folded into one of the four it does know.`,
  }
}

function dimensionCopy(dimension: string): DimensionCopy {
  const raw = dimension.trim()
  const known = Object.hasOwn(DIMENSION_COPY, raw) ? DIMENSION_COPY[raw as IncidentDimension] : undefined
  return known ?? unrecognisedDimension(raw || 'an unnamed dimension')
}

/* --------------------------------------------------------------- degradation */

/**
 * Degraded on either axis, exactly as design §6 defines it: the two are
 * independent by design and a shared cause surfaces on either — a filtering
 * relay lands on health, an authentication fault lands on the lane.
 *
 * Duplicated from the backend's own definition because it has to be: when the
 * incident array comes back empty, nothing in the payload says how many
 * mailboxes were degrading, and "no shared cause found across the degraded
 * mailboxes" is a different sentence from "nothing is degrading".
 */
const DEGRADED_HEALTH: ReadonlySet<string> = new Set(['watch', 'throttled', 'paused'])
const DEGRADED_LANES: ReadonlySet<string> = new Set(['quarantine', 'recovery', 'blocked'])

function isDegraded(mailbox: WarmupMailbox): boolean {
  return DEGRADED_HEALTH.has(mailbox.health_state) || DEGRADED_LANES.has(mailbox.lane)
}

/**
 * Values that are the ABSENCE of a classification rather than one, mirroring the
 * backend's own exclusion. An incident on one of these is not a finding, so it
 * is discarded rather than rendered: a row reading "signing domain: unknown"
 * correlates degraded mailboxes on our own failure to resolve their signature,
 * and it would look exactly like a real finding.
 */
const UNRESOLVED_VALUES: ReadonlySet<string> = new Set(['', 'unknown'])

function isResolved(value: string): boolean {
  return !UNRESOLVED_VALUES.has(value.trim().toLowerCase())
}

/* ------------------------------------------------------------- the reading */

/**
 * The shared value as an operator reads it.
 *
 * A domain is a domain and is shown as it was recorded. A destination is an ESP
 * token, and `microsoft` is the contract's word rather than anyone's provider —
 * so it goes through the matrix's own vocabulary, because the same provider
 * being "Microsoft" in one panel and `microsoft` in another is the defect both
 * panels already forbid separately.
 */
function displayValue(dimension: string, value: string): string {
  const raw = value.trim()
  return dimension === 'destination_route' ? destinationLabel(raw) : raw
}

function incidentReading(incident: WarmupIncident, emailById: Map<string, string>): IncidentReading {
  const copy = dimensionCopy(incident.dimension)
  const value = displayValue(incident.dimension, incident.value)
  return {
    key: `${incident.dimension}:${incident.value.trim()}`,
    dimension: copy.label,
    dimensionDetail: copy.detail,
    value,
    stats: cohortStats(incident),
    // An id we cannot name is shown as an id. Dropping it would leave the list
    // one short of the count beside it, which reads as a rendering that lost a
    // mailbox rather than as a pool that lost one.
    members: incident.member_mailbox_ids.map((id) => emailById.get(id)?.trim() || id),
  }
}

/**
 * The whole panel's reading, from the contract's optional array and the pool it
 * arrived with.
 *
 * The pool is not optional context: an empty array means four different things
 * depending on it, and the payload itself cannot tell them apart. Disabled rows
 * are dropped first — a mailbox that left the pool is not a participant, and
 * counting its last-known health as current degradation would report a search
 * across mailboxes the backend never considered.
 */
export function incidentsReading(
  // Required on a conforming response, so `undefined` here means there is no
  // response yet — loading, or a failed fetch — not a server that omitted it.
  incidents: WarmupOverview['incidents'] | undefined,
  pool: readonly WarmupMailbox[],
  minPool: WarmupOverview['incidents_min_pool'] | undefined,
): IncidentsReading {
  const participants = pool.filter((mailbox) => mailbox.enabled)
  // An absent floor is an absent search. The contract makes incidents_min_pool
  // required, so this is a server that predates correlation or an overview that
  // failed to load — and without knowing the floor this panel cannot honestly say
  // a pool was too small to look, which is one of the four things it exists to say.
  if (!incidents || minPool === undefined || participants.length === 0) return { kind: 'unreported' }

  const reportable = incidents.filter((incident) => isResolved(incident.value))
  if (reportable.length === 0) {
    const degraded = participants.filter(isDegraded).length
    if (degraded === 0) return { kind: 'quiet', message: quietMessage(participants.length) }
    return { kind: 'none-found', message: noneFoundMessage(degraded, participants.length, minPool) }
  }

  const emailById = new Map(participants.map((mailbox) => [mailbox.mailbox_id, mailbox.email]))
  return {
    kind: 'detected',
    incidents: reportable.slice(0, MAX_SHOWN).map((incident) => incidentReading(incident, emailById)),
    truncated: truncationNote(reportable.length - MAX_SHOWN),
  }
}

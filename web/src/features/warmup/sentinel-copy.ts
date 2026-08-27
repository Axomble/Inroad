// The words a sentinel is made of.
//
// Kept out of JSX for the reason `observer-copy.ts`, `incident-copy.ts` and
// `route-copy.ts` give: the wording IS the feature. A sentinel is a mailbox the
// operator controls end to end and is willing to expose to every lane, so a
// degrading mailbox has something dependable to be measured against — and every
// fact this module reports is one that a plausible-sounding rendering turns into
// something it is not.
//
// Four rules, each written here once so it can be tested:
//
//   PEER-ONLY IS NOT BAD EVIDENCE. It is what a healthy pool mostly produces.
//   What it is not is INDEPENDENT: when a mailbox is measured only by its own
//   lane-mates, a shared cause moves both sides of the comparison at once and the
//   reading looks steady while nothing about it is. That is worth telling an
//   operator; it is not a deficiency, and a warning tone turns the ordinary case
//   into a defect to chase.
//
//   CONFIDENCE IS A LABEL, NOT A PENALTY. Nothing is discounted for it. No
//   threshold moves, no sample floor changes, no promotion is withheld.
//   Discounting peer-only evidence would be a threshold change with no
//   calibration behind it, and every prior slice in this subsystem that guessed a
//   threshold had to be walked back (security.md invariants 57-60).
//
//   A POOL WITH NO SENTINELS IS THE ORDINARY CASE, not a misconfiguration. Most
//   self-hosted installations will never designate one and warmup works exactly
//   as it does without them, so the empty state explains what one would buy and
//   does not nag.
//
//   OVERSIZED IS ADVISORY. "More than the advised share of this pool is
//   sentinels" is a note about measurement becoming the network, not a rule being
//   enforced — nothing is refused, because refusing to pair would stop warmup
//   rather than tell the operator something. A pool of one sentinel and nothing
//   else is over the share AND measuring nothing, which is worth saying plainly.
//
// And absent is not false, throughout. A build that does not report sentinels has
// said nothing about this pool; rendering that as "no sentinel is designated"
// answers a question nobody asked.
import type { WarmupMailbox, WarmupOverview } from '@/store/api'

/** The vocabulary the contract constrains an evidence label to. */
export type EvidenceConfidence = NonNullable<WarmupMailbox['evidence_confidence']>

/** The pool-level facts, exactly as the overview publishes them. */
export interface SentinelPoolFacts {
  /** Enabled participants designated as sentinels. Undefined = not reported. */
  count: WarmupOverview['sentinel_count']
  /** The server's advisory verdict. Never recomputed here — it is policy. */
  oversized: WarmupOverview['sentinel_pool_oversized']
  /** The advised ceiling as a fraction, so the note can state the rule it reports. */
  share: WarmupOverview['sentinel_pool_share']
  /** The overview's own rows — the pool the count is a share OF. */
  pool: readonly WarmupMailbox[]
}

export type SentinelPoolReading =
  /**
   * Nothing to report and nothing to say: either the server does not report
   * sentinels (a build predating them) or the workspace has no participants at
   * all. Both render as silence, because "no sentinel is designated" would answer
   * a question this payload never asked.
   */
  | { kind: 'unreported' }
  /** No sentinel designated. The ordinary arrangement, and a real answer. */
  | { kind: 'none'; message: string }
  | {
      kind: 'designated'
      /** How many, of how many, and what that buys. */
      summary: string
      /** The designated mailboxes, named — emails where the pool knows them. */
      sentinels: string[]
      /**
       * The advisory note, present only when the server says the share is
       * exceeded. Null is the ordinary case and means the pool has nothing to
       * be told about its shape.
       */
      advisory: string | null
    }

/** One mailbox's evidence label, and everything needed to read it honestly. */
export interface ConfidenceReading {
  /** The label in an operator's words — `peer_only` never ships. */
  label: string
  /** What it means, and — every time — what it does not change. */
  detail: string
  /** True only when a sentinel contributed. Presentational; nothing gates on it. */
  corroborated: boolean
}

/** The words a designation decision is made of, shown BEFORE the flip. */
export interface DesignationPrompt {
  title: string
  body: string
  confirm: string
}

/* --------------------------------------------------------------- panel copy */

/**
 * The ordinary case, said as the answer it is.
 *
 * No imperative anywhere in it. An empty state that tells an operator to go and
 * designate something has turned a working pool into a to-do item, and most
 * self-hosted installations will never designate a sentinel at all. It explains
 * what one would add — in terms of the same-lane limit visible on the cards below
 * — and prices it in the same breath, because the exposure is the whole reason an
 * operator might decline and an explainer that omits it is a recruitment pitch.
 */
export const SENTINELS_NONE =
  'No sentinel is designated in this pool, which is the ordinary arrangement — most installations never designate one, and warmup works exactly as it does now without them. What one would add: every mailbox may currently exchange warmup mail only within its own lane, so a mailbox on watch is measured only by other mailboxes that are themselves degrading, and a shared cause moves both sides of that comparison at once. A sentinel is a mailbox you control end to end and are willing to expose to every lane, which gives the comparison a fixed point. It is not free: a sentinel receives warmup mail from degrading members that the rest of the pool is shielded from, which is why it is a designation and not a default.'

/**
 * The label-not-penalty rule in the operator's own view, once above the rows
 * rather than on each of them — repeated per mailbox it becomes chrome to skip.
 */
export const SENTINEL_CONFIDENCE_GATES_NOTHING =
  'Evidence confidence is a label, never a penalty. Peer-only evidence is not discounted anywhere: no threshold is raised against it, no sample floor moves, and no promotion is withheld for it. Discounting it would be a threshold change with nothing calibrated behind it — nobody has yet measured what a sentinel observation is worth next to a peer one here, and every earlier guess at a threshold in this subsystem had to be walked back. The label changes what to read into a steady number, not what the engine does with it.'

/** How a designated mailbox is marked on its own card. */
export const SENTINEL_MARK = 'Sentinel'

export const SENTINEL_MARK_DETAIL =
  'Exposed to every lane on purpose, so degrading mailboxes have something dependable to be measured against. A flag, not a lane: its own health state and lane are unaffected by the designation, and it can degrade like any other participant.'

/* --------------------------------------------------------------- the pool */

function mailboxes(count: number): string {
  return `${count} mailbox${count === 1 ? '' : 'es'}`
}

/**
 * The advised share as the server reported it.
 *
 * Never a hardcoded "half". It is a backend policy constant published for exactly
 * this sentence, and a client copy would keep reading "more than half" after the
 * server recalibrated to something else — the drift `incidents_min_pool` is served
 * to avoid.
 */
function advisedShare(share: number | undefined): string {
  if (share === undefined) return 'past the share this build advises'
  return `past the ${Math.round(share * 100)}% of it this build advises`
}

/** Nothing is enforced. Said on every advisory, because a note about a share reads as a limit. */
const NOT_ENFORCED = 'Nothing is enforced and no pairing is refused'

/**
 * What being over the share actually costs, which is not "a limit was hit".
 *
 * Past that share the references stop being a measurement OF the pool and become
 * most of the network the pool is measured against — the sentinels mail each
 * other, and what is left to measure is the minority.
 */
function oversizedNote(sentinels: number, pool: number, share: number | undefined): string {
  if (pool === 1 && sentinels === 1) {
    return `This pool is one sentinel and nothing else. It is ${advisedShare(share)} by definition, and it is also measuring nothing: a sentinel exists to give other mailboxes a fixed point to be compared against, and there are no other mailboxes. ${NOT_ENFORCED} — there is simply nothing here to pair it with yet.`
  }
  if (sentinels === pool) {
    return `Every mailbox in this pool is a sentinel, so every measurement here is one sentinel measuring another and no ordinary participant is left for the references to be a reference for. ${NOT_ENFORCED} — but the arrangement is now measuring itself rather than the pool.`
  }
  return `${sentinels} of ${mailboxes(pool)} in this pool are sentinels, ${advisedShare(share)}. ${NOT_ENFORCED} — this is a note about the shape of the pool, not a rule anything acts on. Past that share the sentinels stop being a measurement of the pool and become most of the network it is measured against: they exchange mail with each other, and what is left to be measured is the minority.`
}

function designatedSummary(sentinels: number, pool: number): string {
  const subject = sentinels === 1 ? 'is designated as a sentinel' : 'are designated as sentinels'
  return `${sentinels} of ${mailboxes(pool)} in this pool ${subject}: exposed to every lane on purpose, so a degrading mailbox has something dependable to be measured against. Each keeps its own health state and its own lane, and can degrade, be contained and recover like any other participant.`
}

/**
 * The pool's whole reading, from the overview's facts and the rows they describe.
 *
 * The advisory verdict is the SERVER's — never recomputed from the count and the
 * pool size here. It is a policy question, and a second opinion about it is the
 * kind of duplicated rule this subsystem keeps having to remove.
 *
 * Disabled rows are dropped first: a mailbox that left the pool is not a
 * participant, and counting its last-known designation would name a sentinel that
 * is exchanging no mail at all.
 */
export function sentinelPoolReading({ count, oversized, share, pool }: SentinelPoolFacts): SentinelPoolReading {
  const participants = pool.filter((mailbox) => mailbox.enabled)
  // Required on a conforming response that reports sentinels, so `undefined` here
  // is a build that does not — not a pool without any. An empty pool is silence
  // for the same reason: there is nothing yet for a reference to be a reference for.
  if (count === undefined || participants.length === 0) return { kind: 'unreported' }
  if (count <= 0) return { kind: 'none', message: SENTINELS_NONE }

  return {
    kind: 'designated',
    summary: designatedSummary(count, participants.length),
    // Named from the rows rather than counted from them: the count is the
    // server's own, and an id we cannot name is still a designation an operator
    // needs to see.
    sentinels: participants
      .filter((mailbox) => mailbox.is_sentinel)
      .map((mailbox) => mailbox.email.trim() || mailbox.mailbox_id),
    advisory: oversized ? oversizedNote(count, participants.length, share) : null,
  }
}

/* --------------------------------------------------------------- confidence */

/**
 * The sentence every reading ends on, in both directions.
 *
 * On the peer-only reading it stops the label being read as a discount; on the
 * corroborated one it stops the mirror-image misreading, that corroboration buys
 * the mailbox something. Both are wrong in the same way, so both get the same
 * ending.
 */
const CHANGES_NOTHING =
  'A label on the evidence and not a score: no threshold moves, nothing is promoted sooner or held back for it, and the rates beside it are counted exactly as they would be either way.'

const PEER_ONLY_DETAIL = `Every placement observation behind these rates came from this mailbox's own lane-mates. That is not bad evidence — it is what a healthy pool mostly produces — but it is not independent: when a mailbox is measured only by mailboxes in the same lane, a shared cause moves both sides of the comparison at once, and the reading looks steady while nothing about it is. It matters most exactly where it is hardest to see, on a mailbox already on watch or in recovery. ${CHANGES_NOTHING}`

function corroboratedDetail(observations: number | undefined): string {
  const opening =
    observations !== undefined && observations > 0
      ? `${observations} of the placement observations behind these rates were filed by a sentinel`
      : 'At least one of the placement observations behind these rates was filed by a sentinel'
  return `${opening} — a mailbox exposed to every lane on purpose — so the reading has a fixed point behind it and not only this mailbox's own lane-mates. ${CHANGES_NOTHING}`
}

/**
 * A label this build has no reading for. The backend's vocabulary is closed; the
 * JSON boundary is not.
 *
 * Named as it arrived, for the reason an unrecognised destination and an
 * unrecognised fault dimension are: folding it into either of the two known labels
 * would state which kind of evidence produced these rates when it does not know.
 */
function unrecognisedConfidence(raw: string): ConfidenceReading {
  return {
    label: `${raw} — a confidence this build does not know`,
    detail: `The server labelled this mailbox's evidence "${raw}". This build has no reading for that label, so it is named as it arrived rather than folded into peer-only or sentinel-corroborated. ${CHANGES_NOTHING}`,
    corroborated: false,
  }
}

/**
 * One mailbox's evidence label, or null on a build that does not report one.
 *
 * The LABEL decides the sentence, not the count: the label is the server's own
 * verdict and the count is the arithmetic behind it, so a payload where they
 * disagree renders the verdict rather than printing a figure that contradicts it.
 * That is also why peer-only never prints its count — it is zero by definition,
 * and "0 sentinel observations" reads as a deficit where there is none.
 */
export function confidenceReading(
  confidence: WarmupMailbox['evidence_confidence'],
  sentinelObservations: WarmupMailbox['sentinel_observations_7d'],
): ConfidenceReading | null {
  if (confidence === undefined) return null
  switch (confidence) {
    case 'peer_only':
      return { label: 'Peer-only', detail: PEER_ONLY_DETAIL, corroborated: false }
    case 'sentinel_corroborated':
      return {
        label: 'Sentinel-corroborated',
        detail: corroboratedDetail(sentinelObservations),
        corroborated: true,
      }
    default:
      return unrecognisedConfidence(String(confidence).trim() || 'an unnamed confidence')
  }
}

/* ------------------------------------------------------------ designating */

/**
 * What an operator is told BEFORE they flip the switch, not after.
 *
 * Designating is a real decision with a real cost: the mailbox starts receiving
 * warmup mail from degrading members that the rest of the pool is shielded from.
 * That is the point of a sentinel — it absorbs the exposure so healthy mailboxes
 * do not — and a control that reveals it afterwards has taken the decision on the
 * operator's behalf.
 *
 * Two things the prompt also has to hold: containment still outranks measurement
 * (a sentinel does not reach into quarantine), and designation says nothing about
 * this mailbox's own standing — it is a flag, not a lane, so a prompt that implied
 * otherwise would read as a demotion.
 */
export function designationPrompt(email: string, next: boolean): DesignationPrompt {
  if (next) {
    return {
      title: `Designate ${email} as a sentinel?`,
      body: 'A sentinel may exchange warmup mail with every lane, not just its own. This mailbox will receive warmup mail from members that are degrading — mail the rest of the pool is shielded from — and that is the cost of giving those members something dependable to be measured against. Containment still comes first: a quarantined or blocked mailbox is withheld from the pool, and a sentinel does not reach into it. Nothing about this mailbox changes otherwise; it keeps its own health state and its own lane, and you can stop using it as a sentinel at any time.',
      confirm: 'Designate as sentinel',
    }
  }
  return {
    title: `Stop using ${email} as a sentinel?`,
    body: 'It goes back to exchanging warmup mail only within its own lane, and stops receiving mail from degrading members. Placement evidence already gathered through it is not removed — the mailboxes it corroborated keep every observation — but corroboration is counted against who is a sentinel now, so those readings become peer-only unless another sentinel is designated.',
    confirm: 'Stop using as sentinel',
  }
}

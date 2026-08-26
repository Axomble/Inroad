// The words a campaign's domain concentration is made of.
//
// Component-free for the reason `sender-draft.ts` is, and written out here for
// the reason `route-copy.ts` and `incident-copy.ts` are: the wording IS the
// feature. A concentration table is trivially easy to render as a compliance
// report, and every sentence of that reading is false. Four rules, in the order
// they are easiest to get wrong:
//
//   THE CEILING IS PER DOMAIN, and it is not always the campaign's. A degrading
//   domain is held to a lower one, so a domain at 25% can be over budget while
//   another at 55% is not. Rendered against one shared limit the table reads as
//   broken arithmetic, and the operator's correct conclusion is that the figures
//   are wrong. Every row therefore carries its OWN ceiling, and says out loud
//   when that ceiling was tightened and why.
//
//   OVER BUDGET IS NOT A FAILURE AND NOT A BLOCK. Nothing is withheld: the
//   budget only shifts which eligible mailbox takes the next contact, and only
//   while a mailbox on another domain is available. It never reduces sending, so
//   red, "violation", "blocked" and "exceeded" are all lies about what happened.
//
//   A SINGLE-DOMAIN POOL IS THE ORDINARY CASE, not an error — it is what every
//   small workspace looks like. One row at 100% over a 0.6 ceiling is a budget
//   with nowhere to shift to, which means it does not apply, which means a
//   warning there is one the operator cannot act on. The actionable sentence is
//   "connect a mailbox on another domain", not "you are over".
//
//   EMPTY IS A REAL ANSWER, and the two ways of being empty are different
//   answers. No contacts assigned yet is "nothing to measure"; contacts assigned
//   with no shared domain among them is "nothing here can fail together".
//   Neither is "balanced".
//
// And two smaller ones that are just as easy to lose: `assigned` counts CONTACTS
// ASSIGNED, never mail sent — a mailbox holds its contacts for the whole
// sequence — and these rows are NOT an exhaustive breakdown of the pool, because
// consumer-provider mailboxes are deliberately excluded from them.
import type { CampaignSenderPool, FaultDomainShare } from './api'

/** How a share reads against its own ceiling. Colour is reinforcement; the label is the signal. */
export type ExposureTone =
  /** Above this domain's own ceiling. Advisory — nothing is withheld. */
  | 'over'
  /** Inside it. */
  | 'within'
  /** The only domain there is, so the budget has nowhere to shift to. */
  | 'inapplicable'

/** One fault domain's row, and everything needed to render it honestly. */
export interface DomainExposure {
  /** Stable per row: the backend reports one entry per domain. */
  key: string
  /** The organizational domain the mailboxes stand under, as it arrived. */
  domain: string
  /** Contacts assigned to mailboxes on this domain. Assigned — never sent. */
  assigned: string
  /** This domain's share of the campaign's assigned contacts. */
  share: string
  /**
   * The ceiling THIS domain was judged against. Not always the campaign's, which
   * is the whole reason the field exists on the wire.
   */
  ceiling: string
  /** The status in words, always — never colour alone. */
  status: string
  tone: ExposureTone
  /**
   * Share and ceiling as whole percents for the meter, or null when the server
   * sent no usable figure. Decoration only: everything it shows is in the text
   * beside it, so the meter is hidden from the accessibility tree.
   */
  meter: { share: number; ceiling: number | null } | null
  /** What the two figures do not say on their own. Null when they say it all. */
  detail: string | null
  /** Why this domain's ceiling is below the campaign's. Null when it is not. */
  tightened: string | null
}

export type ExposureReading =
  /**
   * Nothing to say. No pool has loaded yet, the campaign has no senders at all
   * (the panel already says so, and a pool nobody sends from carries no risk), or
   * the server predates exposure reporting. All three render as silence rather
   * than as a measurement nobody made.
   */
  | { kind: 'unreported' }
  /** No contact has been assigned yet, so there is no concentration to measure. */
  | { kind: 'unassigned'; message: string }
  /** Contacts assigned, and nothing among them that can fail together. */
  | { kind: 'ungrouped'; message: string }
  | {
      kind: 'measured'
      /** Worst first, exactly as the server ordered them. */
      domains: DomainExposure[]
      /**
       * Present only when one domain carries every assigned contact — the
       * reading this panel is most likely to lose, because it is the one that
       * must NOT be a warning. Null when the table stands on its own.
       */
      soleNote: string | null
      /** Said plainly when these rows do not add up to the whole pool. */
      uncovered: string | null
    }

/* --------------------------------------------------------------- panel copy */

/**
 * What the panel says about itself, above the rows.
 *
 * The per-domain-ceiling rule is here rather than only on the rows that carry a
 * tightened one, because the reader who needs it is the one comparing two rows —
 * and by the time they have decided the arithmetic is broken, a footnote under
 * the smaller row is too late.
 */
export const EXPOSURE_INTRO =
  "How much of this campaign rests on any one domain. A domain is a thing that fails all at once: one bad key rotation, one blocklisting, and every mailbox standing under it goes together — so a domain carrying most of the campaign's contacts is a single point of failure. Each domain is measured against its OWN ceiling, which is lower for a domain that is already degrading, so a domain at 25% can be over budget while another at 55% is not."

/**
 * The counterpart to the warmup panels' "gates nothing", and a different
 * sentence because the reason is different. Those report a signal no decision
 * reads. This one reports a budget that IS acted on — it just never acts by
 * withholding, which is the only thing an operator seeing "over budget" will
 * assume it did.
 */
export const EXPOSURE_ADVISORY =
  'Advisory only: none of this withholds a send, pauses a mailbox or slows the campaign down. Being over budget shifts which mailbox takes the NEXT contact, and only while a mailbox on another domain is free to take it — with nowhere else to send, the campaign sends exactly as it did. Contacts already assigned stay on the mailbox that has them.'

/* ------------------------------------------------------------- the absences */

/**
 * No assignments yet. Deliberately not "balanced" and not "no concentration":
 * both claim a measurement over contacts that do not exist, and the first
 * handful of assignments can land entirely on one domain.
 */
const NO_ASSIGNMENTS =
  'No contact has been assigned to a mailbox in this pool yet, so there is no concentration to measure. Not the same as balanced — nothing has been spread across anything. The first assignments decide the shape of this.'

/**
 * Contacts assigned, and nothing groupable among them. A real answer, and the
 * reason for it is the one thing that makes it read as an answer rather than as
 * a missing table.
 */
const NO_SHARED_DOMAIN =
  'No shared domain to report: the contacts assigned so far are on mailboxes that stand under no common domain, so there is nothing here that can fail for several of them at once. Mailboxes at consumer providers — gmail.com and the like — are deliberately not treated as a shared domain, because two strangers at the same provider do not share a fate.'

/* ------------------------------------------------------------ the arithmetic */

/** The server sent no usable number. Not a zero, and not a clean result. */
const NOT_STATED = 'Not stated'

/**
 * A fraction in [0,1] as a percent an operator can read.
 *
 * A positive share that rounds to nothing reads "<1%" and a share short of the
 * whole reads ">99%", for the same reason `formatRoutePct` refuses to print a
 * confident 0%: rounding a real concentration up to 100% would contradict the
 * "these rows do not cover the pool" note printed underneath it.
 *
 * A non-finite value gets words rather than `NaN%`, which on a screen reads as a
 * finding rather than as the rendering fault it is.
 */
function formatShare(value: number): string {
  if (!Number.isFinite(value)) return NOT_STATED
  if (value > 0 && value < 0.005) return '<1%'
  if (value > 0.995 && value < 1) return '>99%'
  return `${Math.round(value * 100)}%`
}

/** Contacts assigned to this domain's mailboxes. The noun is load-bearing. */
function contactsAssigned(count: number): string {
  if (!Number.isFinite(count)) return 'Contacts assigned not stated'
  return `${count.toLocaleString()} contact${count === 1 ? '' : 's'} assigned`
}

/** Whole percent for the meter, clamped — a figure outside [0,1] must not overflow the track. */
function meterPercent(value: number): number | null {
  if (!Number.isFinite(value)) return null
  return Math.min(Math.max(value, 0), 1) * 100
}

/**
 * A domain's share is the whole of the measured volume. Not `=== 1`: the server
 * computes a float division, and a pool of three mailboxes on one domain can
 * arrive as 0.9999999999999999.
 */
const COVERS_ALL = 0.999

/** Float slack when comparing a domain's ceiling against the campaign's. */
const CEILING_EPSILON = 1e-9

/* ------------------------------------------------------------ the row copy */

/**
 * What being over budget actually did, which is very little. Written on the row
 * rather than only in the advisory note because this is the row an operator
 * stops on, and "over budget" with no gloss beside it is read as a stoppage.
 */
const OVER_BUDGET_DETAIL =
  'Over this domain\'s own ceiling, so new contacts go to a mailbox on another domain while one is free. Nothing is paused and no send is withheld.'

/**
 * Why this domain's ceiling is below the campaign's — the sentence that keeps
 * the table from reading as broken arithmetic.
 */
function tightenedCeiling(ceiling: number, campaignMax: number): string | null {
  if (!Number.isFinite(ceiling) || !Number.isFinite(campaignMax)) return null
  if (ceiling >= campaignMax - CEILING_EPSILON) return null
  return `Held to ${formatShare(ceiling)} rather than the campaign's ${formatShare(campaignMax)} because this domain is degrading — which is why it can be over budget at a share a healthy domain would sit well inside.`
}

/**
 * One domain carries every assigned contact.
 *
 * The ordinary shape of a small workspace, so it opens on the dependency rather
 * than on the number: an operator told they are "68% over a 60% limit" on the
 * only domain they own has been given a rule they cannot satisfy. The budget is
 * unsatisfiable here and simply does not apply, and the one action that changes
 * that is the sentence it ends on.
 */
function soleDomainNote(domain: string): string {
  return `This campaign depends entirely on ${domain}: every contact assigned so far is on a mailbox standing under it, so one bad key rotation or one blocklisting there takes the whole campaign with it. The budget has nowhere to shift contacts to while that is true, so it does not apply and nothing is being held back. Connect a mailbox on another domain to spread the risk.`
}

/**
 * These rows are not the whole pool, and the difference has a reason.
 *
 * Suppressed when any share is unusable, because a total computed over a missing
 * figure would name a shortfall that is really our own gap.
 */
function uncoveredNote(rows: readonly FaultDomainShare[]): string | null {
  if (rows.some((row) => !Number.isFinite(row.share))) return null
  const covered = rows.reduce((sum, row) => sum + row.share, 0)
  if (covered >= 0.995) return null
  return `These rows cover ${formatShare(covered)} of the contacts assigned. The rest are on mailboxes that stand under no shared domain — chiefly consumer providers like gmail.com, left out because two strangers at the same provider do not share a fate. Read this as the concentrations that exist, not as a breakdown of the whole pool.`
}

/* -------------------------------------------------------------- the readings */

interface Status {
  status: string
  tone: ExposureTone
  detail: string | null
}

/**
 * The row's verdict in words.
 *
 * The sole-domain row deliberately does NOT say "over budget", even though the
 * server's flag is true and the arithmetic on screen plainly is: a budget with
 * no alternative to shift to did not judge this domain and did not do anything
 * about it. Both figures stay on the row, so nothing is hidden — the note under
 * the table is what the flag means here.
 */
function rowStatus(row: FaultDomainShare, sole: boolean): Status {
  if (sole) return { status: 'Only domain', tone: 'inapplicable', detail: null }
  if (row.over_budget) return { status: 'Over budget', tone: 'over', detail: OVER_BUDGET_DETAIL }
  // No gloss: the share, its own ceiling and the word "within" are the whole fact.
  return { status: 'Within budget', tone: 'within', detail: null }
}

function domainExposure(row: FaultDomainShare, campaignMax: number, sole: boolean): DomainExposure {
  const share = meterPercent(row.share)
  return {
    key: row.domain,
    domain: row.domain,
    assigned: contactsAssigned(row.assigned),
    share: formatShare(row.share),
    ceiling: formatShare(row.ceiling),
    ...rowStatus(row, sole),
    meter: share === null ? null : { share, ceiling: meterPercent(row.ceiling) },
    tightened: tightenedCeiling(row.ceiling, campaignMax),
  }
}

/** Contacts the pool's own rows account for — used only to tell 0 from more than 0. */
function totalAssigned(senders: CampaignSenderPool['senders']): number {
  return senders.reduce((sum, sender) => sum + (Number.isFinite(sender.assigned_count) ? sender.assigned_count : 0), 0)
}

/**
 * The whole panel's reading.
 *
 * `undefined` is the pool not having loaded — the panel's own caller renders a
 * skeleton and an error state, so there is nothing to say here. A response
 * without the array at all is a server predating exposure reporting, and it gets
 * the same silence: "no concentration" would report a measurement nobody made.
 */
export function exposureReading(pool: CampaignSenderPool | undefined): ExposureReading {
  if (!pool || !Array.isArray(pool.fault_domain_shares) || !Array.isArray(pool.senders)) {
    return { kind: 'unreported' }
  }

  const rows = pool.fault_domain_shares
  if (rows.length === 0) {
    // Nothing to send from at all. The panel says that in its own words directly
    // above; a second paragraph about concentration is noise on an empty pool.
    if (pool.senders.length === 0) return { kind: 'unreported' }
    return totalAssigned(pool.senders) === 0
      ? { kind: 'unassigned', message: NO_ASSIGNMENTS }
      : { kind: 'ungrouped', message: NO_SHARED_DOMAIN }
  }

  const first = rows[0]
  const sole = rows.length === 1 && first !== undefined && Number.isFinite(first.share) && first.share >= COVERS_ALL

  return {
    kind: 'measured',
    // Server order is kept: it reports worst first, and re-sorting here would
    // put this panel's idea of "worst" against the one the rotation uses.
    domains: rows.map((row, index) => domainExposure(row, pool.max_fault_domain_share, sole && index === 0)),
    soleNote: sole && first !== undefined ? soleDomainNote(first.domain) : null,
    // Mutually exclusive with the sole note by construction: one domain covering
    // everything leaves nothing uncovered.
    uncovered: sole ? null : uncoveredNote(rows),
  }
}

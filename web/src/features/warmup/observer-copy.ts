// The words an observer-trust verdict is made of.
//
// Kept out of JSX for the reason `incident-copy.ts`, `route-copy.ts` and
// `identity-copy.ts` give: the wording IS the feature. Here it is the WHOLE
// feature, because nothing else in the system reads this field. The backend
// measures which mailboxes report far more of the mail they receive as spam than
// their peers do, publishes each verdict with its arithmetic, and then applies it
// to nothing (security.md invariant 59). So the only thing that happens to a
// verdict is that an operator reads this panel.
//
// Three rules the copy exists to hold, each of which a plausible-sounding
// rendering breaks:
//
//   The FIELD NAME IS NOT THE COPY. The contract calls these observers
//   "discounted", which is the vocabulary of a gate that was built and then
//   removed. "Discounted", "untrusted", "hostile", "blocked" all state that
//   something happened to this mailbox. Nothing happened to it. What the data
//   supports is "it reports far more spam than its peers on the same provider",
//   and that is a suspicion, not a sanction — a legitimately strict receiving
//   provider produces exactly this signal.
//
//   NOTHING IS EXCLUDED, and it has to be said out loud. A list of flagged
//   mailboxes is read as a list of things that were filtered out; an operator who
//   concludes their spam evidence was quietly dropped has been misled by a screen
//   that was technically silent on the matter. The reports still count, and the
//   reason acting on them is deferred belongs on the same screen: the peer
//   comparison is dilutable, so a wrong verdict would silence the mailbox
//   reporting real spam and leave the sender it reported looking cleaner than it
//   is. Under-containment is the dangerous direction.
//
//   The ARITHMETIC is shown, not a badge. 3.1× and 14× are very different
//   findings, and an operator who disagrees with the verdict needs this mailbox's
//   rate over its own count, its peers' rate, and the multiple between them —
//   which is exactly what a chip reading "outlier" destroys.
//
// And `unknown` is NOT a provider, the same way it is not a destination in the
// route matrix and not a fault domain in the incident fold. The cohort is the
// observer's own receiving provider; `unknown` means the MX behind it was never
// resolved, so there is no population the rate was compared against. The backend
// refuses to judge those, and a row that arrived with one anyway is discarded here
// rather than rendered as a fourth provider.
import type { WarmupMailbox, WarmupOverview } from '@/store/api'
import { destinationLabel } from './route-copy'

/** One published verdict exactly as the contract defines it — one source of truth. */
export type WarmupDiscountedObserver = WarmupOverview['discounted_observers'][number]

/** The vocabulary the contract constrains an observer's cohort to. */
export type ObserverCohort = WarmupDiscountedObserver['cohort']

/**
 * A cohort that names a receiving provider. `unknown` is excluded by type, not by
 * a runtime check alone: it is the absence of a comparison population rather than
 * one of the values a comparison can be made against.
 */
type ResolvedCohort = Exclude<ObserverCohort, 'unknown'>

/**
 * One figure behind a verdict, with the label that makes it readable alone. Three
 * per row, and they are the row: without them this is a badge saying "outlier".
 */
export interface ObserverStat {
  /** What the figure counts. */
  label: string
  /** The figure itself — short, so it can be scanned and asserted. */
  value: string
  /**
   * The sentence that keeps `value` from being over-read. Null when the label and
   * the figure already say everything true about it.
   */
  detail: string | null
}

/** One observer, and everything needed to render its verdict without over-stating it. */
export interface ObserverReading {
  /** Stable per row: the backend reports one verdict per observer and cohort. */
  key: string
  /**
   * The observer, named. An email where the pool knows the id, the id itself
   * where it does not — an id is ugly and honest, where rendering nothing would
   * report a verdict about nobody.
   */
  mailbox: string
  /** What its rate was compared against, in an operator's words. */
  comparison: string
  /** Its own rate, its peers' rate, and the multiple — in that order, always all three. */
  stats: ObserverStat[]
  /**
   * Present only on a mailbox that appears more than once, which the contract
   * allows: an observer whose history spans two receiving providers is compared
   * against each separately. Unsaid, the second row reads as a duplicate.
   */
  repeated: string | null
}

export type ObserversReading =
  /**
   * No verdict was published at all — there is no overview yet (loading, or a
   * failed fetch) or the server predates observer trust. Rendered as silence,
   * because "no observer stands out" would claim a comparison nobody ran.
   */
  | { kind: 'unreported' }
  /** The comparison was made and nothing stood out. A real answer. */
  | { kind: 'none'; message: string }
  | { kind: 'flagged'; observers: ObserverReading[] }

/* --------------------------------------------------------------- panel copy */

/**
 * What the panel says about itself, above the rows.
 *
 * The last two sentences are the suspicion-not-sanction rule in the operator's
 * own view, and they are here rather than per row deliberately: repeated under
 * every row they become chrome to skip, said once above them they are read.
 */
export const OBSERVERS_INTRO =
  'Mailboxes that reported far more of the warmup mail they received as spam than their peers on the same receiving provider did. Placement is credited to the sender but recorded by the recipient, so one mailbox with an aggressive filter, a bulk-junked folder or a compromised account makes every sender that mails it look worse than it is. This says nothing about these mailboxes as senders, and it is not proof that any of them is wrong — a legitimately strict provider looks exactly the same from here. The arithmetic is shown so you can judge it yourself.'

/**
 * The sentence this panel exists to carry, and the one the field name argues
 * against: the contract calls these observers "discounted" because a gate was
 * built here and then removed, and a list of flagged mailboxes reads as a list of
 * evidence that was thrown away.
 *
 * Rendered above the rows rather than under them, unlike the "gates nothing" note
 * on every other panel in this feature. Those qualify a number an operator is
 * reading; this one corrects a conclusion they will otherwise have reached by the
 * time they finish the first row.
 */
export const OBSERVERS_NOTHING_EXCLUDED =
  "Nothing is excluded. Every report below still counts as evidence against the senders that mailed these mailboxes, exactly as it did before, and no health state, lane or promotion decision reads any of this. Acting on it is deferred on purpose: the peer comparison is gameable — adding clean volume to a provider's mailboxes drags the peer rate down until an honest mailbox clears the multiple — so a wrong verdict would silence the one reporting real spam and leave the sender it reported looking cleaner than it is. Evidence that makes a sender look worse than it is costs sending and is visible; evidence that makes one look better goes unnoticed. So this is published and acted on by nothing."

/**
 * Nothing stood out, said as the answer it is.
 *
 * The hedge in the last sentence is load-bearing and is as far as this can
 * honestly go. Whether a pool was too small to compare is NOT derivable from this
 * payload: the verdict is computed over seven days of observations grouped by the
 * OBSERVER and its receiving provider, so the population is neither the enabled
 * pool nor anything the overview reports — `placement_sample_7d` counts mail a
 * mailbox SENT, and a mailbox since removed from the pool still has reports in the
 * window. Naming a floor here would state a comparison this side never saw.
 */
export const OBSERVERS_NONE =
  'No mailbox in this pool reported spam far out of line with its peers on the same receiving provider, so every report counted the same as every other — which is an answer, not a gap. It is not a guarantee that none of them is strict: a mailbox is only ever compared against others on its own provider, so one with too few comparable peers, or too few reports of its own, is never judged either way.'

/* ------------------------------------------------------------- the arithmetic */

/**
 * A 0..1 rate to a whole percent.
 *
 * A positive rate that rounds to nothing reads as "<1%", never "0%": a peer rate
 * of half a percent is what makes a multiple large, and printing it as a
 * confident zero turns the one figure that explains the row into a division by
 * nothing. Deliberately not shared with the route matrix's `formatRoutePct` or the
 * card's `formatPct` — both of those answer a null, and these two rates are never
 * null, so folding them together would import a null semantics this module has no
 * use for and would then have to keep true.
 */
function formatRate(rate: number): string {
  if (!Number.isFinite(rate)) return 'not stated'
  if (rate > 0 && rate < 0.005) return '<1%'
  return `${Math.round(rate * 100)}%`
}

/**
 * The multiple as an operator can read it: one decimal where the difference
 * between 3.1 and 3.8 matters, whole numbers once it plainly does not. Same
 * reading `incident-copy` gives a lift, and a non-finite figure gets words for the
 * same reason — `NaN×` on a screen is a rendering bug read as a finding.
 */
function formatMultiple(lift: number): string | null {
  if (!Number.isFinite(lift)) return null
  if (lift >= 10) return `${Math.round(lift)}×`
  return `${lift.toFixed(1)}×`
}

const MULTIPLE_DETAIL =
  "How many times its peers' rate this mailbox reported. 1× would be exactly in line with them."

/**
 * A cohort with no spam at all makes any spam-reporting mailbox infinitely worse
 * than its peers, which is true and unprintable — the backend scores it with half
 * a case instead. Said here because the multiple then has a floor built into it
 * and must not be read as an exact ratio of two rates on screen, one of which is
 * zero.
 */
const CONTINUITY_DETAIL = `${MULTIPLE_DETAIL} Its peers reported no spam at all in this window, so rather than dividing by zero the multiple is scored against half a case — read it as "far above its peers", not as an exact figure.`

function multipleStat(observer: WarmupDiscountedObserver): ObserverStat {
  const value = formatMultiple(observer.lift)
  if (value == null) {
    return {
      label: 'Multiple of its peers',
      value: 'Not stated',
      detail:
        'No usable multiple arrived with this verdict, so the two rates beside it are the whole of the evidence. Not a zero, and not a strong result.',
    }
  }
  return {
    label: 'Multiple of its peers',
    value,
    detail: observer.cohort_spam_rate > 0 ? MULTIPLE_DETAIL : CONTINUITY_DETAIL,
  }
}

/**
 * The three figures, which are the check on the verdict. None means anything
 * alone: 45% of 130 is only out of line while its peers sit at 12%, and it is
 * ordinary while they sit at 40%.
 */
function observerStats(observer: WarmupDiscountedObserver, peers: string): ObserverStat[] {
  return [
    {
      label: 'Called spam, of what it received',
      // Both the count and the rate, because the rate alone hides the sample it
      // was computed over and the count alone makes the reader do the division.
      value: `${observer.spam} of ${observer.total} (${formatRate(observer.spam_rate)})`,
      detail: null,
    },
    {
      label: 'Its peers, over the same window',
      value: formatRate(observer.cohort_spam_rate),
      detail: `The same rate for ${peers}, with this mailbox left out of it — a mailbox that dominates its cohort would otherwise raise the very baseline it is measured against and hide itself.`,
    },
    multipleStat(observer),
  ]
}

/* --------------------------------------------------------------- the cohorts */

/**
 * How each cohort is named mid-sentence, keyed by the contract's own vocabulary
 * minus `unknown` — so a receiving provider added to `api/openapi.yaml` fails to
 * compile until it has words, the guard `DESTINATION_COPY` and `DIMENSION_COPY`
 * give theirs.
 *
 * The provider names themselves come from the route matrix's vocabulary rather
 * than being retyped here: the same provider must not be "Microsoft" in one panel
 * and `microsoft` in another, and one source of truth for that is the point of
 * `destinationLabel` being exported at all.
 *
 * `other` is not a provider but a bag of them — the cohort key is literally
 * "resolved, and neither Google nor Microsoft" — and its phrasing says so, because
 * "other Another provider mailboxes" is not English and "other providers" hides
 * that the peers may be on several different ones.
 */
const COHORT_PEERS: Record<ResolvedCohort, string> = {
  google: `other ${destinationLabel('google')} mailboxes`,
  microsoft: `other ${destinationLabel('microsoft')} mailboxes`,
  other: 'other mailboxes whose provider is neither Google nor Microsoft',
}

/**
 * A cohort this build has no words for. The backend's vocabulary is closed; the
 * JSON boundary is not.
 *
 * Named as it arrived, for the reason an unrecognised destination is: folding it
 * into Google or into the "neither of the two" bag would state which provider this
 * mailbox was compared against when we do not know.
 */
function peersPhrase(cohort: string): string {
  const raw = cohort.trim()
  return Object.hasOwn(COHORT_PEERS, raw)
    ? COHORT_PEERS[raw as ResolvedCohort]
    : `other mailboxes in the same cohort, "${raw}" — a receiving provider this build does not know`
}

/**
 * Cohorts that are the ABSENCE of a comparison population rather than one,
 * mirroring the backend's own exclusion. `unknown` is the destination_esp
 * column's default: the MX behind the observer was never resolved, so there is no
 * population its rate was measured against. A row like that is discarded rather
 * than rendered, because as a fourth provider it would look exactly like a real
 * finding.
 */
const UNRESOLVED_COHORTS: ReadonlySet<string> = new Set(['', 'unknown'])

function hasResolvedCohort(observer: WarmupDiscountedObserver): boolean {
  return !UNRESOLVED_COHORTS.has(observer.cohort.trim().toLowerCase())
}

/* ------------------------------------------------------------- the reading */

function repeatedNote(times: number): string | null {
  if (times < 2) return null
  return 'Listed once for each receiving provider its reports were compared under — the same mailbox, two comparisons, not two mailboxes.'
}

/**
 * The whole panel's reading, from the contract's array and the pool it arrived
 * with.
 *
 * The pool is used for names only. Every mailbox in it is a candidate, disabled
 * ones included: the verdict spans seven days of observations, so a mailbox
 * removed from the pool yesterday still has reports in the window and would
 * otherwise be named by its id while its email sits in the payload.
 */
export function observersReading(
  // Required on a conforming response, so `undefined` here means there is no
  // response yet — loading, or a failed fetch — not a server that omitted it.
  observers: WarmupOverview['discounted_observers'] | undefined,
  pool: readonly WarmupMailbox[],
): ObserversReading {
  if (!observers) return { kind: 'unreported' }

  const reportable = observers.filter(hasResolvedCohort)
  if (reportable.length === 0) return { kind: 'none', message: OBSERVERS_NONE }

  const emailById = new Map(pool.map((mailbox) => [mailbox.mailbox_id, mailbox.email]))
  const rowsPerMailbox = new Map<string, number>()
  for (const observer of reportable) {
    rowsPerMailbox.set(observer.observer_mailbox_id, (rowsPerMailbox.get(observer.observer_mailbox_id) ?? 0) + 1)
  }

  return {
    kind: 'flagged',
    observers: reportable.map((observer) => {
      const peers = peersPhrase(observer.cohort)
      return {
        key: `${observer.observer_mailbox_id}:${observer.cohort.trim()}`,
        mailbox: emailById.get(observer.observer_mailbox_id)?.trim() || observer.observer_mailbox_id,
        comparison: `Compared with ${peers}`,
        stats: observerStats(observer, peers),
        repeated: repeatedNote(rowsPerMailbox.get(observer.observer_mailbox_id) ?? 1),
      }
    }),
  }
}

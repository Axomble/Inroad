// The words the destination-route matrix is made of.
//
// Kept out of JSX for the reason `identity-copy.ts` and `transition-copy.ts`
// give: the wording IS the feature. A route matrix is the most actionable thing
// this subsystem can tell an operator ("your mail to Microsoft is going to
// spam") and, rendered carelessly, the most dishonest — so the four readings it
// can produce are each written once, here, where they can be tested.
//
// The dangerous misreading is the SINGLE-ROUTE one. Warmup partners are the
// workspace's OWN connected mailboxes, so the only destinations measurable are
// the ESPs already in that pool. An all-Google workspace has exactly one route,
// and a tidy one-row matrix would tell its operator that Microsoft delivery is
// healthy when no warmup mail was sent to Microsoft at all. Design §3: a
// single-ESP pool is reported as "only one destination observed", never as a
// matrix that happens to have one clean row.
//
// Two more distinctions this module exists to hold apart:
//
//   `unknown` is NOT a provider. It means the recipient domain's MX has not been
//   resolved yet, so nobody recorded where the mail went. Rendering it as a
//   fourth destination beside Google/Microsoft/Other invents a place.
//
//   `other` IS a provider — resolved, and neither Google nor Microsoft. It is a
//   finding about the destination, not a gap in our lookup, and the two must not
//   render alike. Same discipline `none` vs `unknown` gets on the identity panel.
//
// And a null rate is "not established", never 0%. Splitting a 7-day window by
// destination shrinks every denominator — a four-route pool quarters them — so
// a route below the sample floor is the ordinary case here, not an edge one.
import type { WarmupDetail } from '@/store/api'

/** One row of the matrix exactly as the contract defines it — one source of truth. */
export type WarmupRoute = NonNullable<NonNullable<WarmupDetail['routes']>[number]>

/** The vocabulary the contract constrains a destination to. */
export type DestinationEsp = WarmupRoute['destination_esp']

/**
 * One rate on one route, with everything needed to read it without over-reading
 * it. `population` is never optional: every figure here is computed over THIS
 * route's own count, and two routes' rates are only comparable to each other
 * once both denominators are on screen.
 */
export interface RouteRate {
  /** Which rate: matches the column it sits under. */
  label: string
  /** Words or a percentage — never a bare dash, never an empty cell. */
  value: string
  /** True only when a real measurement stands behind `value`. */
  measured: boolean
  /** The sample `value` was computed over, always this route's own. */
  population: string
  /**
   * The sentence that keeps `value` from being over-read.
   *
   * Null when the value and its population already say everything true about
   * the figure — a measured percentage over a stated sample needs no gloss, and
   * twelve sentences in a four-row matrix would bury the three that matter.
   */
  detail: string | null
}

/** One destination, and everything the matrix needs to render its row honestly. */
export interface RouteReading {
  /** The raw contract value; stable per row because routes aggregate per destination. */
  esp: string
  /** Where the mail went, in words. */
  destination: string
  /** What that destination means, including what it is NOT. */
  destinationDetail: string
  /**
   * Whether we actually know where this mail was delivered. False only for
   * `unknown`, and it is what separates an unresolved lookup from a provider.
   */
  resolved: boolean
  /** How the sole-destination note names this row mid-sentence. */
  inSentence: string
  rates: RouteRate[]
}

export type RoutesReading =
  | { kind: 'unobserved'; message: string }
  | {
      kind: 'observed'
      routes: RouteReading[]
      /**
       * Present only when exactly one destination has been observed — the
       * reading design §3 requires, and the one this feature is most likely to
       * lose. Null means the matrix has more than one row and stands on its own.
       */
      soleNote: string | null
    }

/* --------------------------------------------------------------- panel copy */

/** What the panel says about itself, above the matrix. */
export const ROUTES_INTRO =
  "Where this mailbox's warmup mail was actually delivered over the last 7 days, split by destination provider — decided by the recipient domain's MX, not by how this mailbox sends. Each row is measured only on the mail that took that route."

/**
 * Design §7, and deliberately NOT the sentence the tabbed rate and the identity
 * panel carry. Those gate nothing because their signals are structurally
 * unobservable on a whole provider class, which no amount of data fixes. A route
 * rate is fully observable wherever the route exists; what is missing is
 * calibration — nobody has yet seen what a normal Google-to-Microsoft warmup
 * spam rate looks like in this system. Recording the right reason matters
 * because this condition is meant to expire, and the copied one would not.
 */
export const ROUTES_GATES_NOTHING =
  'Reported for visibility only: no threshold, lane or promotion decision reads any of it. Not because a route cannot be measured — it can, on every provider — but because nobody has yet seen what a normal per-route rate looks like here, so any threshold set today would be a guess. Reading the disparity between rows is left to you.'

/**
 * Nothing has been observed on any destination yet. Said as the absence it is:
 * an empty matrix with column headings reads as four clean routes.
 */
export const ROUTES_UNOBSERVED =
  "No warmup mail from this mailbox has been observed reaching a destination yet, so there is no route to report. That is not a delivery failure: rows appear once a partner polls the mail this mailbox sends."

/* ---------------------------------------------------------------- the rates */

/** The three columns, named once so the headings and the cells cannot drift apart. */
const INBOX_LABEL = 'Inbox 7d'
const SPAM_LABEL = 'Spam 7d'
const TABBED_LABEL = 'Tabbed 7d'

export const ROUTE_RATE_COLUMNS: readonly string[] = [INBOX_LABEL, SPAM_LABEL, TABBED_LABEL]

/**
 * 0..1 to a whole-percent string.
 *
 * A positive rate that rounds to nothing reads as "<1%", never "0%" — a real
 * signal rounded down to a confident zero is the false-clean reading this whole
 * screen keeps having to remove. Deliberately not shared with the card's
 * `formatPct` or the history's `formatBoundedRate`: those two answer a null with
 * "Not measured" and a zero with "Not established" respectively, and folding
 * three different null semantics into one helper is how one of them would
 * quietly acquire another's meaning.
 */
function formatRoutePct(rate: number): string {
  if (rate > 0 && rate < 0.005) return '<1%'
  return `${Math.round(rate * 100)}%`
}

function observations(count: number): string {
  return `${count.toLocaleString()} observation${count === 1 ? '' : 's'}`
}

/**
 * An inbox or spam rate on one route.
 *
 * `null` is the sample floor, not a zero — the route was observed too few times
 * for a rate to mean anything. A measured 0 is the opposite case and stays a
 * measurement, which is why every branch here tests `rate == null` and never the
 * falsiness of the number.
 */
function placementRate(label: string, rate: number | null, samples: number): RouteRate {
  if (samples <= 0) {
    return {
      label,
      value: 'No observations',
      measured: false,
      population: 'nothing was observed on this route',
      detail: 'No mail on this route was observed landing anywhere in the window. An unmeasured route is not a clean one.',
    }
  }
  if (rate == null) {
    return {
      label,
      value: 'Not established',
      measured: false,
      population: `over ${observations(samples)} on this route`,
      detail: `Too few observations on this route to state a rate — not a zero, and not a clean result. Splitting the window by destination shrinks every count, so this is ordinary here rather than exceptional.`,
    }
  }
  return {
    label,
    value: formatRoutePct(rate),
    measured: true,
    population: `of ${observations(samples)} on this route`,
    detail: null,
  }
}

/**
 * The tabbed rate on one route, over its own smaller denominator again: only
 * observations whose reader could see a tab at all count here, so it is not the
 * row's placement sample and must never be read against it.
 *
 * No tab-capable observation on the route is an absence, not a clean primary
 * inbox — the same reading the overview row gives, scoped to one destination.
 */
function tabbedRate(rate: number | null, tabCapable: number): RouteRate {
  if (tabCapable <= 0) {
    return {
      label: TABBED_LABEL,
      value: 'Not detectable',
      measured: false,
      population: 'no tab-capable observations on this route',
      detail:
        "Nothing that received this route's mail could report a category, so no tab was observable here. Not a clean primary-inbox result.",
    }
  }
  if (rate == null) {
    return {
      label: TABBED_LABEL,
      value: 'Not established',
      measured: false,
      population: `over ${tabCapable.toLocaleString()} tab-capable on this route`,
      detail: 'Too few tab-capable observations on this route to state a rate — not a zero.',
    }
  }
  return {
    label: TABBED_LABEL,
    value: formatRoutePct(rate),
    measured: true,
    population: `of ${tabCapable.toLocaleString()} tab-capable on this route`,
    detail: null,
  }
}

/* --------------------------------------------------------- the destinations */

interface DestinationCopy {
  /** The row's name. Words, always — `google` never reaches the screen. */
  label: string
  /** What it means, and for the two that are confusable, what it is not. */
  detail: string
  /** False only for `unknown`: an unresolved lookup is not a place. */
  resolved: boolean
  /** How the sole-destination note names it mid-sentence. */
  inSentence: string
}

/**
 * Keyed by the contract's own union, so a destination added to
 * `api/openapi.yaml` fails to compile until it has copy — the guard
 * `VERDICT_COPY` and `laneMeta` give their vocabularies.
 */
const DESTINATION_COPY: Record<DestinationEsp, DestinationCopy> = {
  google: {
    label: 'Google',
    detail: "Google Workspace or Gmail — where the recipient domain's MX points.",
    resolved: true,
    inSentence: 'Google',
  },
  microsoft: {
    label: 'Microsoft',
    detail: "Microsoft 365 or Outlook — where the recipient domain's MX points.",
    resolved: true,
    inSentence: 'Microsoft',
  },
  other: {
    label: 'Another provider',
    detail:
      'Resolved, and neither Google nor Microsoft — a self-hosted or smaller host. A destination we identified, not one we failed to look up.',
    resolved: true,
    inSentence: 'one provider that is neither Google nor Microsoft',
  },
  unknown: {
    label: 'Destination not resolved',
    detail:
      `The recipient domain's MX has not been resolved yet, so where this mail was delivered was never recorded. Not a provider, and not the same as "Another provider", which means resolved and neither Google nor Microsoft. It fills in once the MX sweep reaches that domain.`,
    resolved: false,
    inSentence: 'a destination that has not been resolved',
  },
}

/**
 * A destination this build has no reading for. The backend's vocabulary is
 * `esp.ESP` and closed, but the JSON boundary is not.
 *
 * Shown as it arrived. Folding it into `unknown` would report a destination we
 * DID resolve as one we failed to look up; folding it into `other` would claim
 * we know it is neither Google nor Microsoft. Same last resort an unrecognised
 * auth verdict and an unrecognised bounce population get.
 */
function unrecognisedDestination(raw: string): DestinationCopy {
  return {
    label: `${raw} — a destination this build does not know`,
    detail: `Mail on this route was delivered to "${raw}". This build has no reading for that destination, so it is shown as it arrived rather than folded into "Another provider" or into an unresolved row — it resolved to something, just not to something named here.`,
    resolved: true,
    inSentence: `"${raw}"`,
  }
}

function destinationCopy(esp: string): DestinationCopy {
  const raw = esp.trim()
  if (!raw) return DESTINATION_COPY.unknown
  const known = Object.hasOwn(DESTINATION_COPY, raw) ? DESTINATION_COPY[raw as DestinationEsp] : undefined
  return known ?? unrecognisedDestination(raw)
}

/* ------------------------------------------------------------- the readings */

function routeReading(route: WarmupRoute): RouteReading {
  const copy = destinationCopy(route.destination_esp)
  return {
    esp: route.destination_esp,
    destination: copy.label,
    destinationDetail: copy.detail,
    resolved: copy.resolved,
    inSentence: copy.inSentence,
    rates: [
      placementRate(INBOX_LABEL, route.inbox_rate_7d, route.placement_sample_7d),
      placementRate(SPAM_LABEL, route.spam_rate_7d, route.placement_sample_7d),
      tabbedRate(route.tabbed_rate_7d, route.tab_capable_sample_7d),
    ],
  }
}

/**
 * The sentence a one-row matrix must carry, and the reason this module has a
 * test named after it.
 *
 * An operator looking at a single green row concludes their delivery is healthy.
 * What the row actually says is that every warmup partner this mailbox has is on
 * one provider, so one provider is the only place its mail has ever been
 * measured going. Both variants open on the same words — "Only one destination
 * observed" — so the limitation is the first thing read, not a footnote under a
 * clean percentage.
 */
function soleDestinationNote(only: RouteReading): string {
  if (!only.resolved) {
    return "Only one destination observed, and it is not resolved — nothing recorded where this mailbox's mail was actually delivered. So this says nothing about delivery to Google, to Microsoft, or to anywhere else, and it is not a matrix. Routes fill in once the MX sweep resolves the recipient domains."
  }
  return `Only one destination observed: ${only.inSentence}. Warmup partners are your own connected mailboxes, so this mailbox's mail has only ever been measured on its way to ${only.inSentence} — this says nothing about how it is delivered to any other provider, and one clean row is not a clean matrix. Connect a mailbox on another provider to measure a second route.`
}

/**
 * The whole matrix's reading, from the contract's optional array.
 *
 * `undefined` — a server too old to report routes — is the same "nothing has
 * been observed" as an empty array. Neither may become a table of headings with
 * no rows, which reads as four destinations that are all fine.
 */
export function routesReading(routes: WarmupDetail['routes']): RoutesReading {
  const rows = routes ?? []
  if (rows.length === 0) return { kind: 'unobserved', message: ROUTES_UNOBSERVED }

  const readings = rows.map(routeReading)
  const only = readings.length === 1 ? readings[0] : undefined
  return { kind: 'observed', routes: readings, soleNote: only ? soleDestinationNote(only) : null }
}

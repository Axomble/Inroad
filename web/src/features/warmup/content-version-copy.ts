// The words the content-version split is made of.
//
// Kept out of JSX for the reason `route-copy.ts` and `observer-copy.ts` give: the
// wording IS the feature. This panel exists to separate two things that until now
// produced the same signal and demand opposite responses — "this thread template
// lands in spam" and "this mailbox is degrading" — and a table of percentages with
// no words around it merges them straight back together.
//
// Three readings this module exists to prevent:
//
//   A rate here is CONFOUNDED, and not in the way the other panels' rates are.
//   Whichever mailboxes happened to draw a template are baked into its apparent
//   spam rate, so a template sent mostly by a degrading mailbox looks like a bad
//   template. That is a second calibration problem sitting on top of the small
//   sample, and it is the whole reason nothing gates on this. An operator who
//   retires a template on one high number has acted on the confound.
//
//   A null rate is "not established", never 0%. The fifth time this rule has had
//   to be applied here, after bounce populations, tab capability, per-route and
//   per-observer. Splitting a shared library across a pool makes small cells the
//   NORMAL case, so most rows will take this branch.
//
//   ONE version is not a comparison. The panel's value is the disparity between
//   templates; a single tidy row invites reading its rate as the library's verdict
//   when nothing was compared to anything. Same trap `route-copy` names for a
//   single-destination pool, and it lands harder here because a new library starts
//   with exactly one version.
//
// Per WORKSPACE, never per mailbox: the library is shared across the pool, so a
// per-mailbox split would quarter an already-thin sample. That is also why this
// panel sits on the pool page and not inside a mailbox card, where every word of it
// would read as a statement about that one sender.
import type { WarmupOverview } from '@/store/api'

/** One row exactly as the contract defines it — one source of truth. */
export type ContentVersion = NonNullable<NonNullable<WarmupOverview['content_versions']>[number]>

/**
 * One figure on one template, with everything needed to read it without
 * over-reading it. `population` is never optional: every rate here is computed
 * over THIS template's own inbox+spam, and two templates' rates are comparable
 * only once both denominators are on screen.
 */
export interface VersionFigure {
  /** Which figure: matches the column it sits under. */
  label: string
  /** Words or a percentage — never a bare dash, never an empty cell. */
  value: string
  /** True only when a real measurement stands behind `value`. */
  measured: boolean
  /** The sample `value` was computed over, always this template's own. */
  population: string
  /**
   * The sentence that keeps `value` from being over-read. Null when the value and
   * its population already say everything true about the figure — a measured
   * percentage over a stated sample needs no gloss.
   */
  detail: string | null
}

/** One template, and everything a row needs to render honestly. */
export interface VersionReading {
  /** Stable list key; the fingerprint is unique per row by construction. */
  key: string
  /** The fingerprint shortened for display. Never presented as a name. */
  label: string
  /** The fingerprint in full, for a title attribute and copy-paste. */
  fingerprint: string
  /** The raw evidence, which survives even when no rate does. */
  counts: string
  figures: VersionFigure[]
}

export type ContentVersionsReading =
  /**
   * No split was published at all — a server predating content versions, or an
   * overview that never arrived. Distinct from `[]`: "nothing observed yet"
   * would describe a window nobody measured.
   */
  | { kind: 'unreported' }
  /** `[]`, which is a real answer and not an empty state to apologise for. */
  | { kind: 'unobserved'; message: string }
  | {
      kind: 'observed'
      versions: VersionReading[]
      /**
       * Present only when exactly one template has been observed. Null means
       * there are at least two rows and the disparity between them is readable.
       */
      soleNote: string | null
    }

/* --------------------------------------------------------------- panel copy */

/** What the panel says about itself, above the rows. */
export const VERSIONS_INTRO =
  "How each template in the warmup library placed over the last 7 days, across the whole pool. Two templates with different spam rates point at the content; one mailbox with a worse rate than its peers points at the mailbox. Each row is measured only on the mail that template produced."

/**
 * The qualifier, and the one that matters most on this panel specifically.
 *
 * Deliberately NOT the sentence `route-copy` carries. A route rate wants
 * calibration and nothing else — the measurement itself is sound. A version rate
 * has a second problem that more data alone does not fix: the mailboxes that drew
 * a template are inside its number. Recording the right reason matters because the
 * route condition is meant to expire and this one only shrinks.
 */
export const VERSIONS_GATES_NOTHING =
  'Reported for visibility only: no threshold, lane or promotion decision reads any of it. Two reasons, not one — the sample per template is small by construction, and whichever mailboxes happened to send a template are baked into its rate. A template sent mostly by a struggling mailbox will look like a bad template. Treat a disparity as somewhere to look, never as a verdict on the content.'

/**
 * Nothing has been observed on any template yet. Said as the absence it is: an
 * empty table with column headings reads as a library with clean rows.
 */
export const VERSIONS_UNOBSERVED =
  'No warmup mail has been observed landing yet, so there is nothing to split by template. That is not a delivery failure: rows appear once partners poll the mail this pool sends.'

/**
 * Exactly one template has been observed — so nothing has been compared, which is
 * the panel's entire purpose. Common rather than exotic: a pool that has only just
 * started sending has drawn one template.
 */
export const VERSIONS_SOLE_NOTE =
  'Only one template has been observed, so there is nothing to compare it against. Its rate describes that template over this pool — it cannot tell you whether the content or the mailboxes produced the result, because separating those is what a second template would do.'

/* -------------------------------------------------------------- the figures */

const INBOX_LABEL = 'Inbox 7d'
const SPAM_LABEL = 'Spam 7d'

/** The columns, named once so headings and cells cannot drift apart. */
export const VERSION_FIGURE_COLUMNS: readonly string[] = [INBOX_LABEL, SPAM_LABEL]

/**
 * 0..1 to a whole-percent string.
 *
 * A positive rate that rounds to nothing reads as "<1%", never "0%" — a real
 * signal rounded down to a confident zero is the false-clean reading this screen
 * keeps having to remove. Deliberately not shared with `route-copy`'s
 * `formatRoutePct` or the card's `formatPct`, for the reason the former states
 * about itself: those answer a null with different words, and folding several null
 * semantics into one helper is how one of them quietly acquires another's meaning.
 */
function formatVersionPct(rate: number): string {
  if (rate > 0 && rate < 0.005) return '<1%'
  return `${Math.round(rate * 100)}%`
}

function observations(count: number): string {
  return `${count.toLocaleString()} observation${count === 1 ? '' : 's'}`
}

/**
 * One rate on one template.
 *
 * `null` is the sample floor, not a zero — the template was observed too few times
 * for a rate to mean anything. A measured 0 is the opposite case and stays a
 * measurement, which is why every branch tests `rate == null` and never the
 * falsiness of the number.
 */
function placementFigure(label: string, rate: number | null | undefined, samples: number): VersionFigure {
  if (samples <= 0) {
    return {
      label,
      value: 'No observations',
      measured: false,
      population: 'nothing produced by this template was observed',
      detail: 'No mail from this template was observed landing anywhere in the window. An unmeasured template is not a clean one.',
    }
  }
  if (rate == null) {
    return {
      label,
      value: 'Not established',
      measured: false,
      population: `over ${observations(samples)} of this template`,
      detail:
        'Too few observations of this template to state a rate — not a zero, and not a clean result. A shared library split across a pool makes small counts ordinary here rather than exceptional.',
    }
  }
  return {
    label,
    value: formatVersionPct(rate),
    measured: true,
    population: `of ${observations(samples)} of this template`,
    detail: null,
  }
}

/* ---------------------------------------------------------- the fingerprint */

/** How much of a fingerprint is enough to tell two rows apart on screen. */
const FINGERPRINT_HEAD = 12

/**
 * A fingerprint shortened for display, with the full value always kept beside it.
 *
 * Truncated rather than hashed into something friendlier, and never replaced with
 * an invented name like "Template 1": the fingerprint is the only handle an
 * operator can use to match a row against the library, and a positional label
 * would renumber itself the moment a template stops being observed.
 */
function shorten(fingerprint: string): string {
  const trimmed = fingerprint.trim()
  if (trimmed.length <= FINGERPRINT_HEAD) return trimmed
  return `${trimmed.slice(0, FINGERPRINT_HEAD)}…`
}

/**
 * An empty fingerprint is a malformed row rather than a template — the JSON
 * boundary is not the backend's closed vocabulary. Shown as an absence so it
 * cannot be mistaken for a template whose name happens to be blank.
 */
const UNIDENTIFIED = 'Template not identified'

/* --------------------------------------------------------------- the reading */

/**
 * Rows in the order they should be READ, which is not the order they arrive in.
 *
 * The API orders by fingerprint (`ORDER BY o.content_version`), which is
 * alphabetical over a hash and therefore carries no meaning — unlike the incidents
 * list, whose strongest-lift-first order is the detector's own and must survive
 * intact. So this sorts, and sorts by SAMPLE descending: the best-evidenced
 * templates first, because those are the only rows that can support a reading.
 *
 * Deliberately not by spam rate. That would rank templates worst-first and present
 * exactly the badness verdict the confound cannot support, with the flimsiest rows
 * — a single spam observation reads as 100% — floating to the top.
 */
function byEvidence(a: ContentVersion, b: ContentVersion): number {
  const sample = (b.placement_sample ?? 0) - (a.placement_sample ?? 0)
  if (sample !== 0) return sample
  // Fingerprint order as the tie-break, so equal-evidence rows hold still between
  // renders rather than swapping on every poll.
  return (a.version ?? '').localeCompare(b.version ?? '')
}

function versionReading(version: ContentVersion, index: number): VersionReading {
  const fingerprint = (version.version ?? '').trim()
  const samples = version.placement_sample ?? 0
  const inbox = version.inbox ?? 0
  const spam = version.spam ?? 0

  return {
    // The fingerprint is unique per row by construction; the index is the fallback
    // for a malformed row that arrived without one, so two blanks stay distinct.
    key: fingerprint || `unidentified-${index}`,
    label: fingerprint ? shorten(fingerprint) : UNIDENTIFIED,
    fingerprint,
    counts: `${inbox.toLocaleString()} inbox, ${spam.toLocaleString()} spam over ${observations(samples)}`,
    figures: [
      placementFigure(INBOX_LABEL, version.inbox_rate, samples),
      placementFigure(SPAM_LABEL, version.spam_rate, samples),
    ],
  }
}

/**
 * The whole panel's reading, from the contract's array.
 *
 * `undefined` and `[]` are different answers and are kept apart here rather than in
 * JSX: the first means nobody measured, the second means we measured and there was
 * nothing. Collapsing them is the mistake the observers and incidents panels each
 * had to be corrected for.
 */
export function contentVersionsReading(
  versions: WarmupOverview['content_versions'] | undefined,
): ContentVersionsReading {
  if (versions == null) return { kind: 'unreported' }
  if (versions.length === 0) return { kind: 'unobserved', message: VERSIONS_UNOBSERVED }

  const rows = [...versions].sort(byEvidence).map(versionReading)
  return {
    kind: 'observed',
    versions: rows,
    soleNote: rows.length === 1 ? VERSIONS_SOLE_NOTE : null,
  }
}

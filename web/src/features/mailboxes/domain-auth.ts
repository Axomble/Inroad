// The state → copy mapping for SPF / DKIM / DMARC, kept out of JSX because the
// wording *is* the feature: this panel's only job is to tell an operator whether
// to go and edit a DNS record, and copy that overstates a problem sends them to
// change records that were already correct.
//
// Three judgements from the API contract's own enum descriptions are encoded
// here rather than left to whoever next touches the markup:
//
//  1. DKIM selectors cannot be enumerated from DNS — the backend probes a list of
//     common ones — so "not found" means "none of our guesses matched", never
//     "you are unsigned". It is advisory and never colours the verdict.
//  2. `unknown` is "we couldn't look it up", not "it's broken".
//  3. DMARC `p=none` is monitoring only. It publishes a record without asking
//     receivers to do anything, so it must not read like enforcement.
import { relativeTime } from '@/lib/relative-time'
import { httpStatus, isFetchBaseQueryError } from '@/lib/rtk-error'
import type { StatusTone } from '@/components/shared/status-pill'
import type { SendingDomain } from '@/store/api'

/**
 * How a single record reads. Deliberately five values, not pass/fail: the two
 * middle ones exist precisely so DKIM-not-detected and DMARC-p=none stop being
 * flattened into "failing".
 *
 * - `pass` — published and doing its job.
 * - `attention` — genuinely missing; the operator must add a DNS record.
 * - `monitoring` — published but not enforcing (DMARC `p=none`).
 * - `advisory` — no signal either way, and that is expected (DKIM).
 * - `unknown` — the lookup didn't answer, or hasn't run yet.
 */
export type CheckVerdict = 'pass' | 'attention' | 'monitoring' | 'advisory' | 'unknown'

/** One record's row copy: a short status plus the sentence that explains it. */
export interface DomainCheck {
  id: 'spf' | 'dkim' | 'dmarc'
  /** The record name, always shown — colour is never the only signal. */
  label: string
  /** Two or three words, e.g. "Published" / "Not detected". */
  status: string
  /** A full sentence; for `attention` it names the record to add. */
  detail: string
  verdict: CheckVerdict
  tone: StatusTone
}

/**
 * Tone per verdict, on the shared StatusPill palette. `advisory` and `unknown`
 * are faint rather than amber/red on purpose — neither is a problem, and a
 * warning colour on "we couldn't check" is the exact misread this panel exists
 * to avoid.
 */
const VERDICT_TONE: Record<CheckVerdict, StatusTone> = {
  pass: 'running',
  attention: 'failing',
  monitoring: 'paused',
  advisory: 'draft',
  unknown: 'draft',
}

/**
 * The one- or two-word form of a verdict, for the collapsed domain line where
 * three records share a row with the domain name and its coverage. The full
 * `status` and `detail` are one disclosure away, so this only has to be
 * unambiguous, not complete — which is why `advisory` reads "no signal" rather
 * than borrowing a word that implies a fault.
 */
const VERDICT_SHORT: Record<CheckVerdict, string> = {
  pass: 'ok',
  attention: 'missing',
  monitoring: 'monitor',
  advisory: 'no signal',
  unknown: 'unchecked',
}

/** Compact status token for a verdict. Rendered uppercase by StatusPill. */
export function shortStatus(verdict: CheckVerdict): string {
  return VERDICT_SHORT[verdict]
}

function check(
  id: DomainCheck['id'],
  label: string,
  verdict: CheckVerdict,
  status: string,
  detail: string,
): DomainCheck {
  return { id, label, verdict, status, detail, tone: VERDICT_TONE[verdict] }
}

/**
 * True when we have no trustworthy answer for a record that wasn't found: the
 * domain-level state is `unknown`, so the lookup either failed or never ran.
 * A record we *did* find is positive evidence and stays a pass regardless.
 */
function couldNotCheck(domain: SendingDomain, found: boolean): boolean {
  return !found && domain.state === 'unknown'
}

function unknownDetail(domain: SendingDomain, record: string): string {
  return domain.checked_at
    ? `The ${record} lookup didn't answer, so this isn't a verdict on your DNS. Recheck to try again.`
    : `This domain hasn't been checked yet. Recheck to look up its ${record} record.`
}

const unknownStatus = (domain: SendingDomain) => (domain.checked_at ? "Couldn't check" : 'Not checked')

export function spfCheck(domain: SendingDomain): DomainCheck {
  if (couldNotCheck(domain, domain.spf.found)) {
    return check('spf', 'SPF', 'unknown', unknownStatus(domain), unknownDetail(domain, 'SPF'))
  }
  if (domain.spf.found) {
    return check(
      'spf',
      'SPF',
      'pass',
      'Published',
      domain.spf.record
        ? `Published at the domain apex: ${domain.spf.record}`
        : 'A v=spf1 record is published at the domain apex.',
    )
  }
  return check(
    'spf',
    'SPF',
    'attention',
    'Not found',
    `Add a TXT record at the apex of ${domain.domain} starting v=spf1 that authorises whatever sends your mail. Without it, bulk receivers can reject or spam-folder every message from this domain.`,
  )
}

export function dmarcCheck(domain: SendingDomain): DomainCheck {
  if (couldNotCheck(domain, domain.dmarc.found)) {
    return check('dmarc', 'DMARC', 'unknown', unknownStatus(domain), unknownDetail(domain, 'DMARC'))
  }
  if (!domain.dmarc.found) {
    return check(
      'dmarc',
      'DMARC',
      'attention',
      'Not found',
      `Add a TXT record at _dmarc.${domain.domain} starting v=DMARC1; p=none to begin monitoring, then tighten to p=quarantine once the reports look clean.`,
    )
  }
  const policy = domain.dmarc.policy
  if (policy === 'quarantine' || policy === 'reject') {
    return check(
      'dmarc',
      'DMARC',
      'pass',
      `Enforcing (p=${policy})`,
      `Published at _dmarc.${domain.domain} with p=${policy}, so receivers act on messages that fail authentication.`,
    )
  }
  // Published but not enforcing. `p=none` and an absent `p=` tag land here
  // together: neither asks a receiver to do anything about a forged message.
  return check(
    'dmarc',
    'DMARC',
    'monitoring',
    'Monitoring only',
    policy === 'none'
      ? `Published at _dmarc.${domain.domain} with p=none, which only collects reports — receivers are not asked to quarantine or reject mail that fails. Move to p=quarantine when your reports look clean.`
      : `Published at _dmarc.${domain.domain} with no p= tag, so receivers are given no instruction — treat it as monitoring only and set p=quarantine when you're ready.`,
  )
}

/**
 * DKIM, which is advisory in both directions. A hit proves signing; a miss
 * proves nothing, because there is no way to list a domain's selectors from
 * DNS — so "not detected" never renders as a failure and never moves the
 * domain's verdict.
 */
export function dkimCheck(domain: SendingDomain): DomainCheck {
  if (domain.dkim.found) {
    return check(
      'dkim',
      'DKIM',
      'pass',
      'Detected',
      domain.dkim.selector
        ? `A signing key is published at ${domain.dkim.selector}._domainkey.${domain.domain}.`
        : 'A DKIM signing key is published for this domain.',
    )
  }
  return check(
    'dkim',
    'DKIM',
    'advisory',
    'Not detected',
    "DKIM selectors can't be discovered from DNS, so we probe the common ones. A correctly signed domain can still show as not detected — this is informational and never counts against the domain.",
  )
}

/** The three record checks, in the order they're read. */
export function domainChecks(domain: SendingDomain): DomainCheck[] {
  return [spfCheck(domain), dkimCheck(domain), dmarcCheck(domain)]
}

/** The domain-level pill: label plus tone. `unknown` is faint, never red. */
export function domainStateLabel(domain: SendingDomain): string {
  if (domain.state === 'passing') return 'Authenticated'
  if (domain.state === 'failing') return 'Action needed'
  return domain.checked_at ? "Couldn't check" : 'Not checked'
}

export function domainStateTone(domain: SendingDomain): StatusTone {
  if (domain.state === 'passing') return 'running'
  if (domain.state === 'failing') return 'failing'
  return 'draft'
}

/**
 * One line under the domain saying what to do next. A failing domain names the
 * records it is missing rather than announcing that something is wrong, because
 * the operator's next action is editing DNS.
 */
export function domainSummary(domain: SendingDomain): string {
  if (domain.state === 'unknown') {
    return domain.checked_at
      ? "The last DNS lookup for this domain didn't answer. That's a problem with the check, not a verdict on your records — recheck to try again."
      : "Not checked yet. Recheck to look up this domain's SPF and DMARC records."
  }
  if (domain.state === 'failing') {
    const missing = [
      domain.spf.found ? null : 'an SPF TXT record at the apex',
      domain.dmarc.found ? null : `a DMARC TXT record at _dmarc.${domain.domain}`,
    ].filter((entry): entry is string => entry !== null)
    return `Add ${missing.join(' and ')} to authenticate this domain.`
  }
  const dmarc = dmarcCheck(domain)
  return dmarc.verdict === 'monitoring'
    ? 'SPF and DMARC are both published, but DMARC is monitoring only — it reports, it does not enforce.'
    : 'SPF and DMARC are published and enforcing.'
}

/** "Never checked" / "Checked 3 hours ago". `now` injectable for tests. */
export function lastCheckedLabel(checkedAt: string | null | undefined, now: number = Date.now()): string {
  return checkedAt ? `Checked ${relativeTime(checkedAt, now)}` : 'Never checked'
}

/** How many mailboxes a domain covers — the reason it's worth fixing. */
export function mailboxCountLabel(count: number): string {
  return count === 1 ? '1 mailbox' : `${count} mailboxes`
}

/**
 * Copy for a failed recheck, narrowed through the shared error seam. A recheck
 * failing says nothing about the domain's records, so none of this phrasing
 * implies a verdict.
 */
export function recheckErrorMessage(error: unknown, domain: string): string {
  const status = httpStatus(error)
  if (status === 404) {
    return `No mailbox in this workspace sends from ${domain} any more — refresh the page.`
  }
  if (status === 429) return 'Too many rechecks in a row — wait a moment and try again.'
  const reason = serverReason(error)
  return reason
    ? `Couldn't check ${domain}: ${reason}. Your DNS records are unaffected.`
    : `Couldn't check ${domain} right now. Your DNS records are unaffected — try again.`
}

/** Copy for a failed load of the domain list. */
export function listErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  return `Couldn't load domain authentication${status ? ` (${status})` : ''}. This says nothing about your DNS — try again.`
}

/** The `{"error": "…"}` envelope the API writes, read through the typed seam. */
function serverReason(error: unknown): string | undefined {
  if (!isFetchBaseQueryError(error)) return undefined
  const reason = (error.data as { error?: string } | undefined)?.error
  return typeof reason === 'string' && reason.length > 0 ? reason : undefined
}

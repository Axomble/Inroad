// The words the observed sending identity is made of.
//
// Kept out of JSX for the reason `transition-copy.ts` gives: the wording IS the
// feature. Every fact on this panel has a plausible-looking rendering that says
// something the data does not support, and two of them are traps:
//
//   `unknown` is NOT a failure. It means no `Authentication-Results` header
//   could be trusted to speak for the receiving system (RFC 8601 §5), so nobody
//   reported a verdict at all. A partner whose provider stamps none is
//   permanently unknown on all three axes; rendering that in a failing tone, or
//   as a dash, tells an operator their authentication is broken when the only
//   thing missing is an observation.
//
//   `none` IS a verdict, and a different one. The receiver looked and found no
//   SPF record / no signature / no DMARC record. It is the receiver's finding
//   about our mail, not a gap in our observation of it, and the two must not
//   render alike.
//
// And nothing here gates anything (design §7): domain authentication is already
// gated, separately and correctly, from DNS we verify ourselves. A `fail` is the
// one verdict an operator would be tempted to act on, so it carries the same
// "· gates nothing" marker the tabbed placement rate does.
import type { WarmupMailbox } from '@/store/api'

/** The identity block exactly as the contract defines it — one source of truth. */
export type WarmupIdentity = NonNullable<WarmupMailbox['identity']>

/** The vocabulary the contract constrains a receiver's answer to. */
export type AuthVerdict = WarmupIdentity['spf_result']

/**
 * One recorded identity fact — a domain — as an operator may safely read it.
 * `recorded` is false when `value` is words standing in for an absent fact, so
 * an absence is never toned as data.
 */
export interface IdentityFact {
  label: string
  value: string
  detail: string
  recorded: boolean
}

/** One receiver verdict, with everything the panel needs to render it honestly. */
export interface VerdictFact {
  /** The check itself: SPF, DKIM, DMARC. */
  label: string
  /** Words, always — never a bare token, never a dash. */
  value: string
  /** The sentence that keeps `value` from being over-read. */
  detail: string
  /**
   * Whether a receiver actually reported this. False only for `unknown`, and it
   * is what separates "nobody looked" from every verdict that means someone did.
   */
  reported: boolean
  /** The one reading that is a genuine negative, and so must say it gates nothing. */
  negative: boolean
  /** Reinforcement only; `value` carries the signal on its own. */
  tone: string
}

export type IdentityReading =
  | { kind: 'unobserved'; message: string }
  | {
      kind: 'observed'
      /** ISO instant, or null when the timestamp is unusable — never a crashing date. */
      observedAt: string | null
      facts: IdentityFact[]
      verdicts: VerdictFact[]
    }

/** What the panel says about itself, above the facts. */
export const IDENTITY_INTRO =
  "What this mailbox's last observed warmup message was signed as, and what the partner that received it reported about those signatures. One observation, not a configuration."

/** Design §7, in the UI as well as in the code, because the temptation is obvious. */
export const IDENTITY_GATES_NOTHING =
  'Reported for visibility only: no threshold, lane or promotion decision reads any of it. Authentication is gated separately, from DNS we verify ourselves.'

/**
 * Said once, when not one verdict came back, rather than three times inside
 * three identical sentences. It is the reading a permanently-unknown mailbox
 * needs and cannot get from any single row: silence across all three checks is
 * a property of the partners that read this mail, so it is stable, expected, and
 * nothing to act on — not three separate blanks that might each fill in later.
 */
export const IDENTITY_NOTHING_REPORTED =
  "None of the partners that received this mailbox's warmup mail stamp authentication results, so all three verdicts stay unreported however well the mail authenticates. Normal for those providers, and not a finding about this mailbox."

/**
 * No observation has carried identity facts yet. Said as the absence it is —
 * five "unknown" chips would claim five checks came back empty when in fact no
 * message has been read at all.
 */
export const IDENTITY_UNOBSERVED =
  'No warmup mail from this mailbox has been observed with identity facts yet, so nothing here is known. That is not a failed check: it fills in once a partner polls the next message this mailbox sends.'

/**
 * How one authentication check is named and described. The per-check phrasing
 * exists because `none` is meaningless in the abstract — "no SPF record", "not
 * signed" and "no DMARC record" are three different findings, and collapsing
 * them into the word "none" hands the reader a token to decode.
 */
interface Mechanism {
  /** The row's label. */
  label: string
  /** What the receiver checked, dropped into the pass/fail/neutral sentences. */
  checked: string
  /** What it found none of, dropped into the `none` sentence. */
  missing: string
  /** `none`, said in full: the finding belongs in the value, not only the detail. */
  noneValue: string
}

const SPF: Mechanism = {
  label: 'SPF',
  checked: 'SPF',
  missing: 'no SPF record for the sending domain',
  noneValue: 'none — no SPF record',
}

const DKIM: Mechanism = {
  label: 'DKIM',
  checked: 'the signature',
  missing: 'no signature to check',
  noneValue: 'none — the mail was not signed',
}

const DMARC: Mechanism = {
  label: 'DMARC',
  checked: 'DMARC',
  missing: 'no DMARC record for the From domain',
  noneValue: 'none — no DMARC record',
}

type VerdictCopy = (mechanism: Mechanism) => Omit<VerdictFact, 'label'>

/**
 * Keyed by the contract's own union, so a verdict added to `api/openapi.yaml`
 * fails to compile until it has copy — the same guard `laneMeta` gives lanes.
 */
const VERDICT_COPY: Record<AuthVerdict, VerdictCopy> = {
  pass: (mechanism) => ({
    value: 'pass',
    detail: `The receiving partner checked ${mechanism.checked} and it passed.`,
    reported: true,
    negative: false,
    tone: 'text-ok',
  }),
  fail: (mechanism) => ({
    value: 'fail',
    detail: `The receiving partner checked ${mechanism.checked} and it failed. Worth investigating, and it changes nothing here — pool eligibility reads authentication from DNS we verify ourselves, never from a header a message carried.`,
    reported: true,
    negative: true,
    tone: 'text-danger',
  }),
  neutral: (mechanism) => ({
    value: 'neutral',
    detail: `The receiving partner checked ${mechanism.checked} and reached no verdict either way. Not a pass, and not a failure.`,
    reported: true,
    negative: false,
    tone: 'text-muted-foreground',
  }),
  none: (mechanism) => ({
    value: mechanism.noneValue,
    detail: `The receiving partner looked and found ${mechanism.missing}. That is the receiver's finding about this mail, not a gap in what we observed.`,
    reported: true,
    negative: false,
    tone: 'text-warn',
  }),
  unknown: (mechanism) => ({
    value: 'not reported by the receiver',
    detail: `Nothing reported a verdict on ${mechanism.checked}: the partner that received this mail stamped no authentication results — an absence of observation, not a failed check.`,
    reported: false,
    negative: false,
    tone: 'text-muted-foreground',
  }),
}

/**
 * A verdict this build has no reading for — `softfail`, `temperror` and friends
 * are real `Authentication-Results` values, and the backend is supposed to fold
 * them to `unknown` before they reach us.
 *
 * If one arrives anyway it is shown as it came. Folding it into `unknown` would
 * report a verdict the receiver DID give as one nobody gave; folding it into
 * `fail` would invent a failure. Same last resort `transition-copy` gives an
 * unrecognised reason code: never silently absorbed into a neighbouring meaning.
 */
function unrecognisedVerdict(mechanism: Mechanism, raw: string): Omit<VerdictFact, 'label'> {
  return {
    value: `${raw} — a verdict this build does not know`,
    detail: `The receiving partner reported "${raw}" on ${mechanism.checked}. This build has no reading for it, so it is shown as it arrived rather than folded into a pass, a failure, or "not reported".`,
    reported: true,
    negative: false,
    tone: 'text-muted-foreground',
  }
}

/**
 * The generated type is a closed union; the JSON boundary is not. An empty or
 * absent verdict is the "nobody said" case, anything else present is something
 * a receiver actually said.
 */
function verdictFact(mechanism: Mechanism, value: string | null | undefined): VerdictFact {
  const raw = value?.trim() ?? ''
  if (!raw) return { label: mechanism.label, ...VERDICT_COPY.unknown(mechanism) }
  const known = Object.hasOwn(VERDICT_COPY, raw) ? VERDICT_COPY[raw as AuthVerdict] : undefined
  const copy = known ? known(mechanism) : unrecognisedVerdict(mechanism, raw)
  return { label: mechanism.label, ...copy }
}

/**
 * Empty means the mail was unsigned, or its signature could not be parsed —
 * §8 of the design makes those the same fact deliberately. Either way the gap
 * gets words: a blank cell reads as a rendering bug, and a dash reads as zero.
 */
function dkimDomainFact(domain: string): IdentityFact {
  const value = domain.trim()
  if (!value) {
    return {
      label: 'DKIM signing domain',
      value: 'Not signed',
      detail:
        'This mail carried no DKIM signature we could read, so nothing signed for it. Unsigned and unparseable are the same fact from here.',
      recorded: false,
    }
  }
  return {
    label: 'DKIM signing domain',
    value,
    detail: 'The d= domain on the signature — who vouched for this mail.',
    recorded: true,
  }
}

function returnPathFact(domain: string): IdentityFact {
  const value = domain.trim()
  if (!value) {
    return {
      label: 'Return-path domain',
      value: 'No return path',
      detail:
        'The mail carried no Return-Path, or a null one — the empty <> a bounce notification uses. No bounce domain was recorded either way.',
      recorded: false,
    }
  }
  return {
    label: 'Return-path domain',
    value,
    detail: 'The exact host of the Return-Path — where bounces for this mail go. Not folded to the organizational domain: a bounce host that fails while its siblings are fine is the distinction worth seeing.',
    recorded: true,
  }
}

/** A timestamp only survives if it can actually be formatted; a NaN date throws. */
function usableInstant(iso: string | null | undefined): string | null {
  if (!iso) return null
  return Number.isNaN(new Date(iso).getTime()) ? null : iso
}

/**
 * The whole panel's reading, from the contract's optional block.
 *
 * `undefined` — a server too old to report identity at all — is the same
 * "nothing has been observed" as an explicit `null`. The alternative is the
 * silent-fallback class this screen has shipped before (an omitted `lane`
 * rendering every mailbox as "Proving"): a missing field must never manufacture
 * five verdicts.
 */
export function identityReading(identity: WarmupMailbox['identity']): IdentityReading {
  if (!identity) return { kind: 'unobserved', message: IDENTITY_UNOBSERVED }
  return {
    kind: 'observed',
    observedAt: usableInstant(identity.observed_at),
    facts: [dkimDomainFact(identity.dkim_domain), returnPathFact(identity.return_path_domain)],
    verdicts: [
      verdictFact(SPF, identity.spf_result),
      verdictFact(DKIM, identity.dkim_result),
      verdictFact(DMARC, identity.dmarc_result),
    ],
  }
}

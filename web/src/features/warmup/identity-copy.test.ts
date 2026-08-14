import { expect, test } from 'vitest'
import {
  IDENTITY_UNOBSERVED,
  identityReading,
  type AuthVerdict,
  type VerdictFact,
  type WarmupIdentity,
} from './identity-copy'

const observed: WarmupIdentity = {
  dkim_domain: 'acme.test',
  return_path_domain: 'bounces.acme.test',
  spf_result: 'pass',
  dkim_result: 'pass',
  dmarc_result: 'pass',
  observed_at: '2026-08-14T09:30:00Z',
}

/** The three verdicts of one reading, keyed by the check they describe. */
function verdicts(identity: WarmupIdentity): Record<string, VerdictFact> {
  const reading = identityReading(identity)
  if (reading.kind !== 'observed') throw new Error('expected an observed reading')
  return Object.fromEntries(reading.verdicts.map((verdict) => [verdict.label, verdict]))
}

function spf(value: string): VerdictFact {
  const fact = verdicts({ ...observed, spf_result: value as AuthVerdict }).SPF
  if (!fact) throw new Error('expected an SPF verdict')
  return fact
}

function facts(identity: WarmupIdentity) {
  const reading = identityReading(identity)
  if (reading.kind !== 'observed') throw new Error('expected an observed reading')
  return Object.fromEntries(reading.facts.map((fact) => [fact.label, fact]))
}

test('a passing verdict reads as a pass, from the receiver', () => {
  const fact = spf('pass')

  expect(fact.value).toBe('pass')
  expect(fact.reported).toBe(true)
  expect(fact.negative).toBe(false)
  expect(fact.tone).toBe('text-ok')
})

// A fail is a real negative and reads as one. It is also the verdict an operator
// is most likely to act on, and acting on it would be wrong: authentication is
// gated from DNS we verify ourselves, never from a header a message carried
// (design §7). Same treatment the tabbed rate gets, for the same reason.
test('a failing verdict reads as a failure that decides nothing', () => {
  const fact = spf('fail')

  expect(fact.value).toBe('fail')
  expect(fact.negative).toBe(true)
  expect(fact.reported).toBe(true)
  expect(fact.tone).toBe('text-danger')
  expect(fact.detail).toMatch(/changes nothing here/i)
  expect(fact.detail).toMatch(/DNS we verify ourselves/i)
})

test('a neutral verdict is neither a pass nor a failure', () => {
  const fact = spf('neutral')

  expect(fact.value).toBe('neutral')
  expect(fact.reported).toBe(true)
  expect(fact.negative).toBe(false)
  expect(fact.detail).toMatch(/not a pass, and not a failure/i)
  expect(fact.tone).not.toBe('text-danger')
})

// THE test most likely to be quietly broken later. `none` is the receiver saying
// it checked and found no record; `unknown` is nobody having said anything at
// all. An implementation that treats every non-pass as "no result" collapses
// them, and the collapse is invisible — both readings look plausible on screen.
test('none and unknown are different readings and never render alike', () => {
  const none = spf('none')
  const unknown = spf('unknown')

  expect(none.value).not.toBe(unknown.value)
  expect(none.detail).not.toBe(unknown.detail)

  // `none` is a finding: someone looked.
  expect(none.reported).toBe(true)
  expect(none.value).toMatch(/no SPF record/i)
  expect(none.detail).toMatch(/looked and found/i)

  // `unknown` is the absence of one: nobody did.
  expect(unknown.reported).toBe(false)
  expect(unknown.value).toBe('not reported by the receiver')
})

// A mailbox whose partners run providers that stamp no Authentication-Results is
// permanently unknown on all three axes. That is our blind spot, not their
// failure — a failing tone here would send an operator to fix authentication
// that was never reported broken.
test('an unreported verdict is never presented as a failure', () => {
  const fact = spf('unknown')

  expect(fact.tone).not.toBe('text-danger')
  expect(fact.negative).toBe(false)
  expect(fact.detail).toMatch(/absence of observation, not a failed check/i)
  // Not a dash, an empty string, or anything else that reads as zero.
  expect(fact.value).toMatch(/[a-z]{3}/i)
  expect(fact.value).not.toMatch(/^[\s—–-]*$/)
})

// The limitation belongs to the partner that received the mail, exactly as the
// tabbed rate's does: identity is extracted from headers the RECIPIENT's poller
// read, so "your provider doesn't support this" points at the wrong mailbox.
test('an unreported verdict does not blame this mailbox\'s own provider', () => {
  const fact = spf('unknown')

  expect(fact.detail).not.toMatch(/this provider|your provider/i)
  expect(fact.detail).toMatch(/partner/i)
})

// "none" alone is a token to decode, and it means three different things across
// the three checks. Each says which nothing was found.
test('none says what was missing, per check', () => {
  const all = verdicts({
    ...observed,
    spf_result: 'none',
    dkim_result: 'none',
    dmarc_result: 'none',
  })

  expect(all.SPF?.value).toMatch(/no SPF record/i)
  expect(all.DKIM?.value).toMatch(/not signed/i)
  expect(all.DMARC?.value).toMatch(/no DMARC record/i)
  // Three findings, three sentences — not one word repeated three times.
  expect(new Set(Object.values(all).map((fact) => fact.value)).size).toBe(3)
})

test('every check reports its own verdict, not the first one', () => {
  const all = verdicts({ ...observed, spf_result: 'pass', dkim_result: 'fail', dmarc_result: 'unknown' })

  expect(all.SPF?.value).toBe('pass')
  expect(all.DKIM?.value).toBe('fail')
  expect(all.DMARC?.reported).toBe(false)
})

// `softfail`, `temperror` and `permerror` are real Authentication-Results values
// the backend is meant to fold to `unknown` before storing. If one reaches the
// UI anyway, both easy answers are lies: "not reported" denies a verdict the
// receiver gave, and "fail" invents one it did not.
test('a verdict this build does not know is shown, not folded into a neighbour', () => {
  const fact = spf('softfail')

  expect(fact.value).toMatch(/softfail/)
  expect(fact.reported).toBe(true)
  expect(fact.negative).toBe(false)
  expect(fact.value).not.toBe('not reported by the receiver')
  expect(fact.value).not.toBe('fail')
})

test('an empty verdict string is the unreported reading, not a blank', () => {
  const fact = spf('')

  expect(fact.value).toBe('not reported by the receiver')
  expect(fact.reported).toBe(false)
})

// Empty means unsigned, or a signature we could not parse — the design makes
// those the same fact. What it never means is "no data here": a blank cell reads
// as a rendering bug and a dash reads as zero.
test('an unsigned message says it was not signed', () => {
  const fact = facts({ ...observed, dkim_domain: '' })['DKIM signing domain']

  expect(fact?.value).toBe('Not signed')
  expect(fact?.recorded).toBe(false)
  expect(fact?.detail).toMatch(/unsigned and unparseable are the same fact/i)
})

test('a whitespace-only signing domain is not presented as a domain', () => {
  const fact = facts({ ...observed, dkim_domain: '   ' })['DKIM signing domain']

  expect(fact?.value).toBe('Not signed')
  expect(fact?.recorded).toBe(false)
})

test('a signed message reports its signing domain as recorded fact', () => {
  const fact = facts(observed)['DKIM signing domain']

  expect(fact?.value).toBe('acme.test')
  expect(fact?.recorded).toBe(true)
})

test('an absent return path is words, not a gap', () => {
  const fact = facts({ ...observed, return_path_domain: '' })['Return-path domain']

  expect(fact?.value).toBe('No return path')
  expect(fact?.recorded).toBe(false)
  expect(fact?.detail).toMatch(/null one/i)
})

// Nothing has been observed. Five "unknown" chips would claim five checks came
// back empty, when in fact no message has been read at all — a different, and
// more alarming, statement than the truth.
test('a mailbox with no observed identity says so instead of reporting verdicts', () => {
  const reading = identityReading(null)

  expect(reading.kind).toBe('unobserved')
  if (reading.kind !== 'unobserved') throw new Error('unreachable')
  expect(reading.message).toBe(IDENTITY_UNOBSERVED)
  expect(reading.message).toMatch(/not a failed check/i)
  expect(reading.message).toMatch(/has been observed with identity facts yet/i)
})

// The field is optional in the contract, so a server that predates it omits it
// entirely. That has to read exactly like an explicit null: the silent fallback
// that shipped once here (an omitted `lane` rendering every card as "Proving")
// is this exact shape.
test('a payload with no identity field at all reads as unobserved', () => {
  expect(identityReading(undefined)).toEqual(identityReading(null))
})

test('an observed identity carries the instant it was seen', () => {
  const reading = identityReading(observed)

  expect(reading.kind).toBe('observed')
  if (reading.kind !== 'observed') throw new Error('unreachable')
  expect(reading.observedAt).toBe('2026-08-14T09:30:00Z')
})

// Intl throws on an invalid date, and a panel that throws tells the operator
// nothing at all. An unusable timestamp drops the timestamp, not the facts.
test('an unusable observation time does not take the facts down with it', () => {
  const reading = identityReading({ ...observed, observed_at: 'not-a-date' })

  expect(reading.kind).toBe('observed')
  if (reading.kind !== 'observed') throw new Error('unreachable')
  expect(reading.observedAt).toBeNull()
  expect(reading.verdicts).toHaveLength(3)
})

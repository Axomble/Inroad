import { expect, test } from 'vitest'
import type { WarmupMailbox } from '@/store/api'
import {
  SENTINEL_CONFIDENCE_GATES_NOTHING,
  confidenceReading,
  designationPrompt,
  sentinelPoolReading,
  type SentinelPoolReading,
} from '../sentinel-copy'

function mailbox(overrides: Partial<WarmupMailbox> = {}): WarmupMailbox {
  return {
    mailbox_id: 'mb-1',
    email: 'one@acme.test',
    enabled: true,
    health_state: 'healthy',
    health_reason: '',
    lane: 'healthy',
    lane_reason: '',
    today_sent: 4,
    today_target: 4,
    placement_sample_7d: 40,
    inbox_rate_7d: 0.9,
    spam_rate_7d: 0.1,
    ...overrides,
  }
}

/** A pool of `size` participants, the first `sentinels` of them designated. */
function pool(size: number, sentinels: number): WarmupMailbox[] {
  return Array.from({ length: size }, (_, i) =>
    mailbox({ mailbox_id: `mb-${i + 1}`, email: `mb${i + 1}@acme.test`, is_sentinel: i < sentinels }),
  )
}

function reading(size: number, sentinels: number, share = 0.5): SentinelPoolReading {
  const members = pool(size, sentinels)
  return sentinelPoolReading({
    count: sentinels,
    oversized: size > 0 && sentinels / size > share,
    share,
    pool: members,
  })
}

function designated(size: number, sentinels: number, share = 0.5) {
  const result = reading(size, sentinels, share)
  if (result.kind !== 'designated') throw new Error(`expected a designated pool, got ${result.kind}`)
  return result
}

function noneMessage(size: number): string {
  const result = reading(size, 0)
  if (result.kind !== 'none') throw new Error(`expected an empty pool reading, got ${result.kind}`)
  return result.message
}

/* ------------------------------------------------------------ the pool facts */

// Absent and zero are different facts. A server that never mentions sentinels has
// said nothing about this pool, and "no sentinel is designated" would answer a
// question it was never asked.
test('a build that does not report sentinels renders nothing at all', () => {
  expect(sentinelPoolReading({ count: undefined, oversized: undefined, share: undefined, pool: pool(3, 0) })).toEqual({
    kind: 'unreported',
  })
})

// The rule this module exists for, half one: no sentinels is the ORDINARY
// arrangement. Most self-hosted installations will never designate one.
test('a pool with no sentinels is the ordinary case, never a misconfiguration', () => {
  const message = noneMessage(4)

  expect(message).toMatch(/ordinary|usual arrangement/i)
  expect(message).toMatch(/warmup works|works exactly as/i)
  // No nagging, no correction, no call to action. An empty state that tells an
  // operator to go and do something has made a working pool into a to-do item.
  expect(message).not.toMatch(/misconfigur|you should|we recommend|recommended|set one up|needs? a sentinel/i)
  expect(message).not.toMatch(/\bwarning\b|\bfix\b|\berror\b/i)
})

// It explains rather than nags: what a sentinel WOULD buy, in terms of the
// same-lane limitation an operator can already see on the cards below.
test('the empty state explains what a sentinel would buy, in terms of the lane limit', () => {
  const message = noneMessage(4)

  expect(message).toMatch(/own lane/i)
  expect(message).toMatch(/watch/i)
  expect(message).toMatch(/measured/i)
})

// And it prices it in the same breath. The cost is the whole reason an operator
// might decline, so an explainer that omits it is a recruitment pitch.
test('the empty state states the exposure a sentinel would take on', () => {
  expect(noneMessage(4)).toMatch(/receives? (warmup )?mail from .*degrading|exposure|exposed/i)
})

test('a designated pool reports the count and names the mailboxes carrying it', () => {
  const result = designated(6, 2)

  expect(result.sentinels).toEqual(['mb1@acme.test', 'mb2@acme.test'])
  expect(result.summary).toMatch(/2 of 6/)
})

/* ------------------------------------------------------------- the advisory */

test('a pool within the advisory share carries no advisory at all', () => {
  expect(designated(6, 2).advisory).toBeNull()
})

// Advisory, not a violation. Exceeded it is reported and nothing is refused —
// refusing to pair would stop warmup rather than tell the operator something.
test('an oversized pool is advised, never sanctioned', () => {
  const advisory = designated(5, 4).advisory ?? ''

  expect(advisory).toMatch(/nothing is (enforced|refused)|no pairing is refused/i)
  expect(advisory).not.toMatch(/violat|not allowed|forbidden|rejected|invalid/i)
})

// What the cap is actually about: past that share the references stop measuring
// the pool and become most of the network being measured.
test('the advisory says what being oversized costs, not that a limit was hit', () => {
  expect(designated(5, 4).advisory ?? '').toMatch(/measurement|network/i)
})

// The share is a served policy constant. A hardcoded "half" would keep reading
// "more than half" after the server recalibrated to something else.
test('the advisory states the share the server reported, not a hardcoded half', () => {
  expect(designated(5, 4, 0.6).advisory ?? '').toContain('60%')
  expect(designated(5, 4, 0.6).advisory ?? '').not.toContain('50%')
})

// Oversized AND useless, which is worth saying plainly rather than reporting the
// share breach on a pool that is measuring nothing whatsoever.
test('a pool of one sentinel and nothing else is called out as measuring nothing', () => {
  const advisory = designated(1, 1).advisory ?? ''

  expect(advisory).toMatch(/nothing (else|to measure)|no other mailbox|measuring nothing/i)
  expect(advisory).toMatch(/nothing is (enforced|refused)|no pairing is refused/i)
})

test('a pool that is entirely sentinels says so rather than reporting a share', () => {
  const advisory = designated(4, 4).advisory ?? ''

  expect(advisory).toMatch(/every mailbox|all four|entirely/i)
  expect(advisory).toMatch(/measur/i)
})

/* --------------------------------------------------------- evidence confidence */

test('a build that does not report confidence renders nothing', () => {
  expect(confidenceReading(undefined, undefined)).toBeNull()
})

// The rule this module exists for, half two. Peer-only is what a healthy pool
// mostly produces; rendered as a deficiency it becomes a defect to chase.
test('peer-only evidence is never rendered as a deficiency', () => {
  const peer = confidenceReading('peer_only', 0)

  expect(peer?.corroborated).toBe(false)
  expect(peer?.label).not.toMatch(/peer_only/)
  expect(peer?.detail).toMatch(/healthy pool|most of it|not (bad|weak)/i)
  expect(peer?.detail).not.toMatch(/insufficient|unreliable|\bpoor\b|\bweak\b|\bwarning\b|\bproblem\b/i)
})

// What it IS: not independent. A shared cause moves both sides of the comparison
// at once, so the reading looks steady while nothing about it is.
test('peer-only names the shared cause that makes it dependent, not weak', () => {
  const detail = confidenceReading('peer_only', 0)?.detail ?? ''

  expect(detail).toMatch(/independent/i)
  expect(detail).toMatch(/shared cause|both sides/i)
  expect(detail).toMatch(/own lane|lane-mates/i)
})

test('a corroborated reading names how many observations came from a sentinel', () => {
  const corroborated = confidenceReading('sentinel_corroborated', 12)

  expect(corroborated?.corroborated).toBe(true)
  expect(corroborated?.label).not.toMatch(/sentinel_corroborated/)
  expect(corroborated?.detail).toContain('12')
})

// The label is the server's verdict and the count is its arithmetic. A build that
// sends one without the other must not print "0" or "undefined" as a figure.
test('corroboration with no count still reads as corroboration', () => {
  const detail = confidenceReading('sentinel_corroborated', undefined)?.detail ?? ''

  expect(detail).toMatch(/at least one/i)
  expect(detail).not.toMatch(/undefined|\bNaN\b|\b0 /)
})

// A label, not a penalty: no threshold moves, nothing is withheld. Said on BOTH
// readings, because "corroborated" invites the mirror-image misreading that
// corroboration buys the mailbox something.
test('neither label changes what the engine does with the evidence', () => {
  for (const label of [confidenceReading('peer_only', 0), confidenceReading('sentinel_corroborated', 3)]) {
    expect(label?.detail).toMatch(/no threshold|nothing is discounted|counted exactly/i)
    expect(label?.detail).toMatch(/promot/i)
  }
  expect(SENTINEL_CONFIDENCE_GATES_NOTHING).toMatch(/label|not a penalty/i)
  expect(SENTINEL_CONFIDENCE_GATES_NOTHING).toMatch(/calibrat/i)
})

// The backend's vocabulary is closed; the JSON boundary is not. Folding an
// unknown label into either of the two would state which kind of evidence this is.
test('an unrecognised confidence is named as it arrived, never folded into either label', () => {
  const odd = confidenceReading('third_party_corroborated' as never, 4)

  expect(odd?.label).toContain('third_party_corroborated')
  expect(odd?.corroborated).toBe(false)
  expect(odd?.detail).toMatch(/does not know|no reading/i)
})

/* ------------------------------------------------------- designating one */

// The rule: designating is a real decision with a cost, and the operator is told
// BEFORE the flip, not after.
test('designating names the exposure it buys before the flip', () => {
  const prompt = designationPrompt('one@acme.test', true)

  expect(prompt.title).toContain('one@acme.test')
  expect(prompt.body).toMatch(/receive/i)
  expect(prompt.body).toMatch(/degrading/i)
  expect(prompt.body).toMatch(/shielded|the rest of the pool is not|other mailboxes are not/i)
  expect(prompt.confirm).toMatch(/sentinel/i)
})

// A flag, not a lane: designation says nothing about this mailbox's own standing,
// and a prompt that implied otherwise would read as a demotion.
test('designating states that the mailbox keeps its own health and lane', () => {
  expect(designationPrompt('one@acme.test', true).body).toMatch(/own (health|lane)/i)
})

// Containment outranks measurement. Without this the prompt reads as opening the
// mailbox to quarantined senders too.
test('designating says containment still holds', () => {
  expect(designationPrompt('one@acme.test', true).body).toMatch(/quarantin|blocked|contain/i)
})

test('undesignating says what becomes of the evidence already gathered', () => {
  const prompt = designationPrompt('one@acme.test', false)

  expect(prompt.title).toContain('one@acme.test')
  expect(prompt.body).toMatch(/already|existing/i)
  expect(prompt.body).toMatch(/peer-only|own lane/i)
  expect(prompt.confirm).not.toMatch(/^Designate/)
})

import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import type { WarmupMailbox } from '@/store/api'
import { WarmupObserversPanel } from '../warmup-observers-panel'
import type { WarmupDiscountedObserver } from '../observer-copy'

/**
 * A published verdict whose arithmetic adds up: 59 of 130 is 45%, its peers sit at
 * 12%, and 45/12 is the 3.8× the server rounded. A fixture whose numbers disagree
 * tests a rendering the backend cannot produce.
 */
function observer(overrides: Partial<WarmupDiscountedObserver> = {}): WarmupDiscountedObserver {
  return {
    observer_mailbox_id: 'mb-1',
    cohort: 'microsoft',
    spam: 59,
    total: 130,
    spam_rate: 0.45,
    cohort_spam_rate: 0.12,
    lift: 3.8,
    ...overrides,
  }
}

function participant(overrides: Partial<WarmupMailbox> = {}): WarmupMailbox {
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
    inbox_rate_7d: 0.98,
    spam_rate_7d: 0.02,
    ...overrides,
  }
}

function renderPanel(observers: WarmupDiscountedObserver[] | undefined, pool: WarmupMailbox[] = [participant()]) {
  return render(<WarmupObserversPanel observers={observers} pool={pool} />)
}

/**
 * The panel through the accessibility tree, which is how it is reached in the
 * browser too — a named region rather than a class or a test id.
 */
function panel() {
  return screen.queryByRole('region', { name: /spam reporting outliers/i })
}

/** The panel as an operator reads it: all its text, in one string. */
function panelText(): string {
  return document.querySelector('[data-slot="warmup-observers"]')?.textContent ?? ''
}

/**
 * The observers named, scoped to the mailbox node alone.
 *
 * Compared on that node rather than the row deliberately: every row also carries a
 * finding label, a comparison sentence, three figures and their explanations, so a
 * mailbox name that silently collapsed to an id — or to nothing — still reads
 * plausibly once the surrounding text is swept into the comparison.
 */
function mailboxes(): string[] {
  return [...document.querySelectorAll('[data-slot="observer-mailbox"]')].map((node) => node.textContent ?? '')
}

/** What each row says it was compared against, without the row's figures. */
function comparisons(): string[] {
  return [...document.querySelectorAll('[data-slot="observer-comparison"]')].map((node) => node.textContent ?? '')
}

/** The figures in order, without their labels or their explanatory sentences. */
function stats(): string[] {
  return [...document.querySelectorAll('[data-slot="observer-stat"]')].map((node) => node.textContent ?? '')
}

/* ------------------------------------------------------------------- flagged */

// The whole point of the row: which mailbox, what it was compared against, and
// the three figures — not a chip saying "outlier". An operator who disagrees with
// the verdict needs the arithmetic, and each figure is meaningless without the
// other two.
test('a published verdict renders the mailbox, its comparison and its arithmetic', () => {
  renderPanel([observer()])

  expect(mailboxes()).toEqual(['one@acme.test'])
  expect(comparisons()).toEqual(['Compared with other Microsoft mailboxes'])
  expect(stats()).toEqual(['59 of 130 (45%)', '12%', '3.8×'])
})

// The rule the field name argues against. `discounted_observers` is the vocabulary
// of a gate that was built and then removed, and every word in that family states
// that something happened to this mailbox. Nothing happened to it.
test('the verdict reads as a suspicion, never as a sanction', () => {
  renderPanel([observer()])

  expect(panelText()).toMatch(/reporting more spam than its peers/i)
  expect(panelText()).toMatch(/not proof that any of them is wrong/i)
  // A legitimately strict provider produces exactly this signal, which is why the
  // row is a comparison rather than a judgement.
  expect(panelText()).toMatch(/a legitimately strict provider looks exactly the same/i)
  expect(panelText()).not.toMatch(/untrusted|not trusted|hostile|discounted|blocked|removed|dropped|penalis|penaliz/i)
})

// The conclusion a list of flagged mailboxes produces on its own — "my spam
// evidence was thrown away" — and the one thing this panel must not leave an
// operator holding, because nothing was thrown away.
test('the panel says plainly that nothing is excluded and the reports still count', () => {
  renderPanel([observer()])

  const note = document.querySelector('[data-slot="observers-nothing-excluded"]')?.textContent ?? ''
  expect(note).toMatch(/nothing is excluded/i)
  expect(note).toMatch(/still counts as evidence/i)
  expect(note).toMatch(/no health state, lane or promotion decision reads any of this/i)
  // And why acting on it is deferred, which is the part that keeps "nothing is
  // excluded" from reading as an oversight nobody noticed.
  expect(note).toMatch(/the peer comparison is gameable/i)
  expect(note).toMatch(/leave the sender it reported looking cleaner than it is/i)
})

// Above the rows, not under them: the others in this feature qualify a figure
// being read, where this corrects a conclusion the first row has already produced.
test('the nothing-is-excluded note precedes the rows it qualifies', () => {
  renderPanel([observer()])

  const note = document.querySelector('[data-slot="observers-nothing-excluded"]')
  expect(note).not.toBeNull()
  // The list of rows is the note's next sibling, so the note cannot drift below it
  // — which is where every other qualifier in this feature sits.
  expect(note?.nextElementSibling?.tagName).toBe('UL')
  expect(note?.nextElementSibling?.querySelectorAll('[data-slot="observer-mailbox"]')).toHaveLength(1)
})

// A cohort is a receiving provider, and `microsoft` is our contract's word rather
// than anyone's provider. `other` is not a provider at all but a bag of them, so
// it says so instead of borrowing a provider's phrasing.
test('every cohort is named in the operator’s language, never as a contract token', () => {
  renderPanel(
    [
      observer({ observer_mailbox_id: 'mb-1', cohort: 'google' }),
      observer({ observer_mailbox_id: 'mb-2', cohort: 'microsoft' }),
      observer({ observer_mailbox_id: 'mb-3', cohort: 'other' }),
    ],
    [
      participant({ mailbox_id: 'mb-1', email: 'one@acme.test' }),
      participant({ mailbox_id: 'mb-2', email: 'two@acme.test' }),
      participant({ mailbox_id: 'mb-3', email: 'three@acme.test' }),
    ],
  )

  expect(comparisons()).toEqual([
    'Compared with other Google mailboxes',
    'Compared with other Microsoft mailboxes',
    'Compared with other mailboxes whose provider is neither Google nor Microsoft',
  ])
  // Case-sensitive: "Microsoft" is the provider, `microsoft` is the column value.
  expect(panelText()).not.toMatch(/google|microsoft/)
})

// The peer rate is what makes a multiple large, and a peer rate under half a
// percent printed as a confident 0% turns the row's explanation into a division by
// nothing.
test('a peer rate that rounds to nothing is not printed as a clean zero', () => {
  renderPanel([observer({ cohort_spam_rate: 0.004, lift: 30 })])

  expect(stats()).toEqual(['59 of 130 (45%)', '<1%', '30×'])
})

// A spotless cohort makes any spam-reporting mailbox infinitely worse than its
// peers, which is true and unprintable — so the backend scores it against half a
// case, and the multiple must not be read as an exact ratio of 45% to 0%.
test('a cohort with no spam at all says the multiple is scored, not divided', () => {
  const spotless = renderPanel([observer({ cohort_spam_rate: 0, lift: 12 })])

  expect(stats()).toEqual(['59 of 130 (45%)', '0%', '12×'])
  expect(panelText()).toMatch(/rather than dividing by zero/i)
  spotless.unmount()

  // And the note is not boilerplate under every row: a cohort that reported spam
  // was divided by a real rate.
  renderPanel([observer()])
  expect(panelText()).not.toMatch(/rather than dividing by zero/i)
})

// `NaN×` on a screen is a rendering bug read as a finding. The two rates beside it
// carry the row perfectly well, so the absence gets words rather than a number.
test('an unusable multiple is stated as missing rather than printed', () => {
  renderPanel([observer({ lift: Number.NaN })])

  expect(stats()).toEqual(['59 of 130 (45%)', '12%', 'Not stated'])
  expect(panelText()).not.toMatch(/nan|infinity/i)
})

// The contract allows one mailbox to appear once per receiving provider its
// reports were compared under. Unsaid, the second row reads as a duplicate — or
// worse, as two mailboxes.
test('a mailbox compared under two providers is two comparisons, and says so', () => {
  renderPanel([observer({ cohort: 'microsoft' }), observer({ cohort: 'google', lift: 4.2 })])

  expect(mailboxes()).toEqual(['one@acme.test', 'one@acme.test'])
  expect(comparisons()).toEqual([
    'Compared with other Microsoft mailboxes',
    'Compared with other Google mailboxes',
  ])
  expect(document.querySelectorAll('[data-slot="observer-repeated"]')).toHaveLength(2)
  expect(panelText()).toMatch(/the same mailbox, two comparisons, not two mailboxes/i)
})

// One row per mailbox is the ordinary case, and hanging "the same mailbox twice"
// on it would explain a repetition that is not there.
test('a mailbox listed once carries no repeated-comparison note', () => {
  renderPanel([observer()])

  expect(document.querySelectorAll('[data-slot="observer-repeated"]')).toHaveLength(0)
})

// An id we cannot name is shown as an id: a verdict rendered about nobody is worse
// than an ugly one. The observer need not be an enabled participant at all — the
// window spans seven days, so a mailbox since removed from the pool still has
// reports in it.
test('an observer the pool cannot name is identified by its id, not dropped', () => {
  renderPanel([observer({ observer_mailbox_id: 'mb-gone' })], [participant()])

  expect(mailboxes()).toEqual(['mb-gone'])
  expect(stats()).toEqual(['59 of 130 (45%)', '12%', '3.8×'])
})

// A mailbox that left the pool is still named by the email in the payload: the
// verdict is about reports it filed while it was in it.
test('a disabled participant is still named by its email', () => {
  renderPanel([observer()], [participant({ enabled: false })])

  expect(mailboxes()).toEqual(['one@acme.test'])
})

/* ------------------------------------------------------------ no verdicts */

// Empty is a real answer, and the one thing the copy must not do is invent the
// floor it cannot see: the comparison is computed over observations grouped by the
// OBSERVER, which is a population this payload never reports.
test('an empty array is an answer rather than an empty state, and invents no floor', () => {
  renderPanel([])

  expect(panel()).toBeInTheDocument()
  expect(mailboxes()).toEqual([])
  expect(stats()).toEqual([])
  expect(panelText()).toMatch(/reported spam far out of line with its peers/i)
  expect(panelText()).toMatch(/which is an answer, not a gap/i)
  // It says what it cannot rule out, without stating a sample floor or a cohort
  // size this side never received.
  expect(panelText()).toMatch(/too few comparable peers, or too few reports of its own/i)
  expect(panelText()).not.toMatch(/\d+\s*(observations|mailboxes|peers|participants)/i)
})

// A cohort of `unknown` is the MX behind the observer never having been resolved,
// so there is no population its rate was compared against. Rendered as a fourth
// provider it would look exactly like a real finding.
test('an unresolved cohort never renders as a provider', () => {
  renderPanel([observer({ cohort: 'unknown' })])

  expect(mailboxes()).toEqual([])
  expect(stats()).toEqual([])
  expect(panelText()).not.toMatch(/unknown/i)
  // And the panel falls back to the honest reading, not to silence.
  expect(panelText()).toMatch(/which is an answer, not a gap/i)
})

// One resolved verdict beside an unresolved one still reports the resolved one:
// discarding the row must not discard the panel.
test('an unresolved cohort is dropped without taking the resolved verdict with it', () => {
  renderPanel(
    [observer({ observer_mailbox_id: 'mb-1', cohort: 'unknown' }), observer({ observer_mailbox_id: 'mb-2' })],
    [participant({ mailbox_id: 'mb-1' }), participant({ mailbox_id: 'mb-2', email: 'two@acme.test' })],
  )

  expect(mailboxes()).toEqual(['two@acme.test'])
  expect(panelText()).not.toMatch(/unknown/i)
})

// A server that publishes no verdicts has made no comparison, so the panel says
// nothing at all. "No mailbox stands out" here would claim a comparison nobody
// ran — the same silent-fallback class as an omitted `lane` rendering every
// mailbox as "Proving".
test('a server that does not report observer trust renders no panel', () => {
  renderPanel(undefined)

  expect(panel()).not.toBeInTheDocument()
  expect(panelText()).toBe('')
})

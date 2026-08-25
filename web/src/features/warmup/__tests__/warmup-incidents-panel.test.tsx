import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import type { WarmupMailbox } from '@/store/api'
import { WarmupIncidentsPanel } from '../warmup-incidents-panel'
import type { WarmupIncident } from '../incident-copy'

function incident(overrides: Partial<WarmupIncident> = {}): WarmupIncident {
  return {
    dimension: 'signing_domain',
    value: 'mail.acme.test',
    member_mailbox_ids: ['mb-1', 'mb-2', 'mb-3', 'mb-4'],
    cohort_size: 5,
    degraded_inside: 4,
    cohort_outside: 20,
    degraded_outside: 1,
    lift: 16,
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

function pool(size: number, overrides: (index: number) => Partial<WarmupMailbox> = () => ({})): WarmupMailbox[] {
  return Array.from({ length: size }, (_, i) =>
    participant({ mailbox_id: `mb-${i + 1}`, email: `mb${i + 1}@acme.test`, ...overrides(i) }),
  )
}

const MIN_POOL = 4

function renderPanel(
  incidents: WarmupIncident[] | undefined,
  participants: WarmupMailbox[],
  minPool: number | undefined = MIN_POOL,
) {
  return render(<WarmupIncidentsPanel incidents={incidents} pool={participants} minPool={minPool} />)
}

/**
 * The panel through the accessibility tree, which is how it is reached in the
 * browser too — a named region rather than a class or a test id.
 */
function panel(): HTMLElement {
  return screen.getByRole('region', { name: /correlated degradation/i })
}

/** The panel as an operator reads it: all its text, in one string. */
function panelText(): string {
  return document.querySelector('[data-slot="warmup-incidents"]')?.textContent ?? ''
}

/**
 * One correlation's rendered nodes, scoped to the value node alone.
 *
 * Compared on the value rather than the row deliberately: every row carries a
 * dimension label, three figures and an explanatory sentence, so a row whose
 * value silently collapsed to something else still reads plausibly when the
 * surrounding text is swept into the comparison.
 */
function values(): string[] {
  return [...document.querySelectorAll('[data-slot="incident-value"]')].map((node) => node.textContent ?? '')
}

function dimensions(): string[] {
  return [...document.querySelectorAll('[data-slot="incident-dimension"]')].map((node) => node.textContent ?? '')
}

function stats(): string[] {
  return [...document.querySelectorAll('[data-slot="incident-stat"]')].map((node) => node.textContent ?? '')
}

function members(): string[] {
  return [...document.querySelectorAll('[data-slot="incident-members"]')].map((node) => node.textContent ?? '')
}

/* ------------------------------------------------------------------ detected */

// The whole point of the row: the dimension, the shared value, the arithmetic on
// both sides, and the mailboxes it names — not a badge saying "incident".
test('a detected correlation renders its dimension, its value and its arithmetic', () => {
  renderPanel([incident()], pool(25, (i) => (i < 4 ? { health_state: 'paused' } : {})))

  expect(dimensions()).toEqual(['signing domain (DKIM)'])
  expect(values()).toEqual(['mail.acme.test'])
  expect(stats()).toEqual(['4 of 5', '1 of 20', '16×'])
  expect(members()).toEqual(['mb1@acme.test, mb2@acme.test, mb3@acme.test, mb4@acme.test'])
})

// Each of the four dimensions reaches the screen in the operator's language. The
// contract token is the thing that must never appear: `signing_domain` is a
// column name, and an operator looking for their DKIM record does not have one.
test('all four dimensions render in words, never as contract tokens', () => {
  renderPanel(
    [
      incident({ dimension: 'destination_route', value: 'microsoft' }),
      incident({ dimension: 'signing_domain', value: 'mail.acme.test' }),
      incident({ dimension: 'return_path_domain', value: 'bounces.acme.test' }),
      incident({ dimension: 'sender_domain', value: 'acme.test' }),
    ],
    pool(25, (i) => (i < 4 ? { health_state: 'paused' } : {})),
  )

  expect(dimensions()).toEqual(['destination', 'signing domain (DKIM)', 'return path', 'sender domain'])
  expect(panelText()).not.toMatch(/destination_route|signing_domain|return_path_domain|sender_domain/)
})

// §8: a lift of 2.1 and a lift of 12 are very different findings, and the badge
// that says "incident" hides the difference. Both figures render, and only the
// marginal one is hedged.
test('a marginal concentration and a strong one render as different findings', () => {
  renderPanel(
    [
      incident({ dimension: 'signing_domain', value: 'strong.test', lift: 12 }),
      incident({ dimension: 'sender_domain', value: 'marginal.test', lift: 2.1 }),
    ],
    pool(25, (i) => (i < 4 ? { health_state: 'paused' } : {})),
  )

  // Both concentrations are on screen, and they are not the same figure.
  expect(stats()).toEqual(['4 of 5', '1 of 20', '12×', '4 of 5', '1 of 20', '2.1×'])
  // The hedge appears once, under the marginal one.
  expect(screen.getAllByText(/read it as a hint/i)).toHaveLength(1)
})

test('the panel says it gates nothing, on the same screen as the rows', () => {
  renderPanel([incident()], pool(25, (i) => (i < 4 ? { health_state: 'paused' } : {})))

  expect(panelText()).toMatch(/no threshold, lane or promotion decision reads any of it/i)
  expect(panelText()).toMatch(/does not say the shared value is why/i)
})

// A silent cap is a lie about how much was found.
test('correlations beyond the cap are counted rather than dropped silently', () => {
  const many = [1, 2, 3, 4, 5, 6].map((n) =>
    incident({ dimension: 'signing_domain', value: `d${n}.test`, lift: 10 - n }),
  )
  renderPanel(many, pool(25, (i) => (i < 4 ? { health_state: 'paused' } : {})))

  expect(values()).toEqual(['d1.test', 'd2.test', 'd3.test', 'd4.test'])
  expect(document.querySelector('[data-slot="incident-truncated"]')?.textContent).toMatch(
    /2 weaker correlations are not shown/i,
  )
})

/* -------------------------------------------------------------- no incidents */

// The pair the contract cannot tell apart: `incidents: []` is byte-identical
// whether the pool is degrading or perfectly quiet, and the two are different
// answers. Asserted together, because the defect is not what either says alone —
// it is the two saying the same thing.
test('an empty array reads differently over a degrading pool than over a quiet one', () => {
  const degrading = renderPanel([], pool(9, (i) => (i < 4 ? { health_state: 'throttled' } : {})))
  expect(panelText()).toMatch(/4 mailboxes are degrading/i)
  expect(panelText()).toMatch(/no shared cause found/i)
  expect(panelText()).not.toMatch(/no degradation in the pool/i)
  degrading.unmount()

  renderPanel([], pool(9))
  expect(panelText()).toMatch(/no degradation in the pool/i)
  expect(panelText()).toMatch(/nothing to correlate/i)
  expect(panelText()).not.toMatch(/no shared cause found/i)
})

// Neither empty answer renders as a row: no dimension, no value, no arithmetic.
test('an empty array renders no correlation rows at all', () => {
  renderPanel([], pool(9, (i) => (i < 4 ? { health_state: 'throttled' } : {})))

  expect(panel()).toBeInTheDocument()
  expect(values()).toEqual([])
  expect(stats()).toEqual([])
  expect(panelText()).not.toMatch(/gates nothing|no threshold, lane or promotion/i)
})

// Grouping on a value that means "we never resolved this" correlates degraded
// mailboxes on our own ignorance. It must not reach the screen as a fault domain
// — and the pool then reads as what it is.
test('an unresolved value never renders as a fault domain', () => {
  renderPanel([incident({ value: 'unknown' })], pool(9, (i) => (i < 4 ? { health_state: 'paused' } : {})))

  expect(values()).toEqual([])
  expect(panelText()).not.toMatch(/unknown/i)
  expect(panelText()).toMatch(/no shared cause found/i)
})

/* ------------------------------------------------------------------- silence */

// A server that does not report incidents has made no inference, so the panel
// says nothing at all. "No shared cause found" here would claim a search nobody
// ran — the same silent-fallback class as an omitted `lane` rendering every
// mailbox as "Proving".
test('a server that does not report incidents renders no panel', () => {
  renderPanel(undefined, pool(9, (i) => (i < 4 ? { health_state: 'paused' } : {})))

  expect(screen.queryByRole('region', { name: /correlated degradation/i })).not.toBeInTheDocument()
  expect(panelText()).toBe('')
})

test('a workspace with no participants renders no panel', () => {
  renderPanel([], [])

  expect(screen.queryByRole('region', { name: /correlated degradation/i })).not.toBeInTheDocument()
})

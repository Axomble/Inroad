import { screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import WarmupRoutesPanel from './warmup-routes-panel'
import type { WarmupRoute } from './route-copy'

const jsonHeaders = { 'content-type': 'application/json' }

const participant = {
  mailbox_id: 'mb-1',
  enabled: true,
  start_volume: 4,
  max_volume: 40,
  ramp_increment: 2,
  reply_rate: 0.3,
  health_state: 'healthy',
  health_reason: '',
  lane: 'healthy',
  started_at: '2026-08-01T00:00:00Z',
  today_sent: 2,
  today_target: 4,
}

function route(overrides: Partial<WarmupRoute> = {}): WarmupRoute {
  return {
    destination_esp: 'google',
    placement_sample_7d: 120,
    inbox_rate_7d: 0.98,
    spam_rate_7d: 0.02,
    tabbed_rate_7d: 0.1,
    tab_capable_sample_7d: 60,
    ...overrides,
  }
}

/** Answer the detail request with a payload, or with a failure. */
let respond: () => Response

beforeEach(() => {
  respond = () =>
    new Response(JSON.stringify({ participant, series: [], routes: [] }), { status: 200, headers: jsonHeaders })
  vi.stubGlobal('fetch', vi.fn(async () => respond()))
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function withRoutes(...routes: WarmupRoute[]) {
  respond = () =>
    new Response(JSON.stringify({ participant, series: [], routes }), { status: 200, headers: jsonHeaders })
}

/** The panel as an operator reads it: all its text, in one string. */
function panelText(): string {
  return document.querySelector('[data-slot="warmup-routes"]')?.textContent ?? ''
}

/**
 * One destination's row, found through the name the matrix gives it.
 *
 * Rows are located by their rendered destination rather than by the contract
 * token, so a row that quietly renders `unknown` as a provider cannot be found
 * under the name it should have had.
 */
function rowFor(destination: string | RegExp): HTMLElement {
  const match = [...document.querySelectorAll('tbody tr')].find((row) => {
    const name = row.querySelector('[data-slot="route-destination"]')?.textContent ?? ''
    return typeof destination === 'string' ? name === destination : destination.test(name)
  })
  if (!match) throw new Error(`no route row rendered for ${String(destination)}`)
  return match as HTMLElement
}

/**
 * The rendered destination name alone, without the sentence explaining it.
 *
 * Compared on its own deliberately: `unknown`'s explanation deliberately mentions
 * "Another provider" to say what it is NOT, so an assertion over the whole cell
 * would call a collapse of the two a difference — and the collapse is the defect.
 */
function destinationOf(row: HTMLElement): string {
  return row.querySelector('[data-slot="route-destination"]')?.textContent ?? ''
}

/**
 * One row's rate values, in column order, without their populations or their
 * explanatory sentences. Scoped to the value node for the same reason: a row
 * whose spam rate silently became "Not established" still reads plausibly when
 * the surrounding sample text is included in the comparison.
 */
function ratesOf(row: HTMLElement): string[] {
  return [...row.querySelectorAll('[data-slot="route-rate"]')].map((cell) => cell.textContent ?? '')
}

function populationsOf(row: HTMLElement): string[] {
  return [...row.querySelectorAll('[data-slot="route-population"]')].map((cell) => cell.textContent ?? '')
}

function soleNote(): string {
  return document.querySelector('[data-slot="route-sole-destination"]')?.textContent ?? ''
}

test('the matrix is loading before it arrives', () => {
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  expect(screen.getByText(/loading destination routes/i)).toBeInTheDocument()
})

// A failed load and an empty pool are different facts. Rendering the failure as
// "nothing observed" would tell an operator their mailbox has reached no
// destination when in truth nothing is known.
test('a failed request says so and never reads as an unobserved pool', async () => {
  respond = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/server had a problem/i)
  expect(panelText()).not.toMatch(/no route to report/i)
  expect(document.querySelector('tbody tr')).toBeNull()
  expect(screen.getByRole('button', { name: /try again/i })).toBeInTheDocument()
})

// Column headings over an empty body read as four destinations that are all fine.
test('an unobserved mailbox says so instead of rendering an empty matrix', async () => {
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  expect(await screen.findByText(/no route to report/i)).toBeInTheDocument()
  expect(document.querySelector('table')).toBeNull()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

// The headline case: one destination clean, another not. A pooled rate reports one
// blended number that understates the Microsoft problem and slanders the Google
// route, so the two rows must be visibly different figures.
test('a clean route and a failing one are shown as two different readings', async () => {
  withRoutes(
    route({ destination_esp: 'google', placement_sample_7d: 400, inbox_rate_7d: 0.99, spam_rate_7d: 0.01 }),
    route({
      destination_esp: 'microsoft',
      placement_sample_7d: 60,
      inbox_rate_7d: 0.45,
      spam_rate_7d: 0.55,
      tabbed_rate_7d: null,
      tab_capable_sample_7d: 0,
    }),
  )
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  expect(ratesOf(rowFor('Google'))).toEqual(['99%', '1%', '10%'])
  expect(ratesOf(rowFor('Microsoft'))).toEqual(['45%', '55%', 'Not detectable'])
})

// Every figure is measured over its own destination's observations. Without the
// denominators an operator compares 55% over 60 with 1% over 400 as though they
// were the same population.
test('each row states the sample its own rates were measured over', async () => {
  withRoutes(
    route({ destination_esp: 'google', placement_sample_7d: 400, tab_capable_sample_7d: 300 }),
    route({ destination_esp: 'microsoft', placement_sample_7d: 60, tab_capable_sample_7d: 12 }),
  )
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  expect(populationsOf(rowFor('Google'))).toEqual([
    'of 400 observations on this route',
    'of 400 observations on this route',
    'of 300 tab-capable on this route',
  ])
  expect(populationsOf(rowFor('Microsoft'))).toEqual([
    'of 60 observations on this route',
    'of 60 observations on this route',
    'of 12 tab-capable on this route',
  ])
})

// Splitting the window by destination shrinks every count, so a route below the
// floor is ordinary. A clean 0% on the one destination with too little evidence
// is the reading an operator would act on.
test('a route below the sample floor is not established, and shows no percentage', async () => {
  withRoutes(
    route({ destination_esp: 'google' }),
    // Every count on the sparse route is sparse. Tab-capable observations are a
    // SUBSET of this route's placements by construction, so leaving the default
    // 60 beside a placement sample of 4 would be a shape the server cannot
    // produce — and the row would then legitimately print a tabbed percentage,
    // making the "no percentage" assertion below fail on a contradiction in the
    // fixture rather than on anything the panel got wrong.
    route({
      destination_esp: 'microsoft',
      placement_sample_7d: 4,
      inbox_rate_7d: null,
      spam_rate_7d: null,
      tabbed_rate_7d: null,
      tab_capable_sample_7d: 2,
    }),
  )
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  const microsoft = rowFor('Microsoft')
  expect(ratesOf(microsoft).slice(0, 2)).toEqual(['Not established', 'Not established'])
  // Scoped to the value nodes: the row legitimately prints "4 observations", and
  // the Google row above legitimately prints percentages.
  for (const value of ratesOf(microsoft)) expect(value).not.toMatch(/%/)
  expect(microsoft).toHaveTextContent(/not a zero/i)
  expect(microsoft).toHaveTextContent(/4 observations on this route/)
})

// The inverse, and the one a "falsy means unknown" implementation gets wrong.
test('a measured zero renders as 0%, not as an absence', async () => {
  withRoutes(route({ destination_esp: 'google', spam_rate_7d: 0, tabbed_rate_7d: 0 }))
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  expect(ratesOf(rowFor('Google'))).toEqual(['98%', '0%', '0%'])
  expect(rowFor('Google')).not.toHaveTextContent(/not established|not detectable/i)
})

// `unknown` means the recipient domain's MX has not been resolved. Rendering it
// beside Google and Microsoft as though it were a fourth destination invents a
// place mail was delivered to.
test('an unresolved destination is not rendered as a provider', async () => {
  withRoutes(route({ destination_esp: 'google' }), route({ destination_esp: 'unknown' }))
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  const unresolved = rowFor(/not resolved/i)
  expect(destinationOf(unresolved)).toMatch(/destination not resolved/i)
  expect(destinationOf(unresolved)).not.toMatch(/provider|google|microsoft/i)
  expect(unresolved).toHaveTextContent(/has not been resolved yet/i)
  // The raw token never reaches the screen.
  expect(panelText()).not.toMatch(/\bunknown\b/)
})

// The two the browser is most likely to flatten into one another. `other` is a
// resolved destination that is neither Google nor Microsoft; `unknown` is a
// lookup that has not happened. Asserted on the names alone, because the
// unresolved row's explanation quotes "Another provider" precisely to disown it.
test('a resolved-but-unnamed destination and an unresolved one do not read alike', async () => {
  withRoutes(route({ destination_esp: 'other' }), route({ destination_esp: 'unknown' }))
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  const other = rowFor('Another provider')
  const unresolved = rowFor(/not resolved/i)

  expect(destinationOf(other)).not.toBe(destinationOf(unresolved))
  expect(other).toHaveTextContent(/resolved, and neither Google nor Microsoft/i)
  expect(other).not.toHaveTextContent(/has not been resolved yet/i)
})

// Colour is never the only signal, and neither is it the only redundancy: a
// resolved destination carries a filled node and an unresolved one a hollow node,
// so the distinction survives a glance and a monochrome screen.
test('an unresolved destination is marked by shape, not by colour alone', async () => {
  withRoutes(route({ destination_esp: 'google' }), route({ destination_esp: 'unknown' }))
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  expect(rowFor('Google').querySelector('[aria-hidden="true"]')?.className).toContain('bg-current')
  const unresolved = rowFor(/not resolved/i).querySelector('[aria-hidden="true"]')
  expect(unresolved?.className).toContain('bg-transparent')
  expect(unresolved?.className).toContain('border')
})

// THE test design §3 calls the one most likely to be quietly broken later. An
// all-Google pool has one route, and a tidy single-row matrix would tell its
// operator that Microsoft delivery is healthy when no mail went to Microsoft.
test('a single-destination pool is warned about, above the row it qualifies', async () => {
  withRoutes(route({ destination_esp: 'google', inbox_rate_7d: 1, spam_rate_7d: 0 }))
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  expect(soleNote()).toMatch(/only one destination observed/i)
  expect(soleNote()).toMatch(/says nothing about how it is delivered to any other provider/i)
  expect(soleNote()).toMatch(/one clean row is not a clean matrix/i)

  // Above the matrix, not under it: a footnote arrives after the wrong
  // conclusion has already been drawn from a green row.
  const note = document.querySelector('[data-slot="route-sole-destination"]')
  const table = document.querySelector('table')
  if (!note || !table) throw new Error('the sole-destination note and the matrix must both render')
  expect(note.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
})

// Warning about a single-route pool that isn't one trains an operator to skip the
// note in the case where it matters.
test('a matrix with two destinations carries no single-route warning', async () => {
  withRoutes(route({ destination_esp: 'google' }), route({ destination_esp: 'microsoft' }))
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  expect(document.querySelector('[data-slot="route-sole-destination"]')).toBeNull()
  expect(panelText()).not.toMatch(/only one destination observed/i)
})

// Design §7, and deliberately not the sentence slices A and B carry: a route rate
// IS observable everywhere, so the reason it decides nothing is that nobody has
// calibrated one yet. That condition is meant to expire; "cannot be observed"
// would outlive it.
test('the panel says it gates nothing, and gives the calibration reason', async () => {
  withRoutes(route())
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  expect(panelText()).toMatch(/no threshold, lane or promotion decision reads any of it/i)
  expect(panelText()).toMatch(/nobody has yet seen what a normal per-route rate looks like/i)
})

// A matrix is a matrix to a screen reader only if each cell is tied to both its
// destination and its rate.
test('every row is headed by its destination and every column by its rate', async () => {
  withRoutes(route({ destination_esp: 'google' }), route({ destination_esp: 'microsoft' }))
  renderWithProviders(<WarmupRoutesPanel mailboxId="mb-1" />)

  await screen.findByRole('table')
  expect(screen.getAllByRole('columnheader').map((header) => header.textContent)).toEqual([
    'Destination',
    'Inbox 7d',
    'Spam 7d',
    'Tabbed 7d',
  ])
  expect(screen.getAllByRole('rowheader')).toHaveLength(2)
})

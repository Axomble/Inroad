import { screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { WarmupTransition } from '@/store/api'
import WarmupTransitionsPanel from '../warmup-transitions-panel'

const jsonHeaders = { 'content-type': 'application/json' }

function transition(overrides: Partial<WarmupTransition> = {}): WarmupTransition {
  return {
    id: 't-1',
    created_at: '2026-08-12T10:00:00Z',
    from_state: 'healthy',
    to_state: 'healthy',
    reason_code: 'health_unchanged',
    reason: 'health is unchanged; this transition moves the pool lane',
    placement_samples: 40,
    spam_rate: 0.02,
    bounce_samples: 200,
    bounce_rate: 0,
    complaint_samples: 0,
    complaint_rate: 0,
    invalid_tokens: 0,
    policy_version: 'warmup-phase1-v1',
    ...overrides,
  }
}

/** Answer the transitions request with a payload, or with a failure. */
let respond: () => Response

beforeEach(() => {
  respond = () => new Response(JSON.stringify({ transitions: [] }), { status: 200, headers: jsonHeaders })
  vi.stubGlobal('fetch', vi.fn(async () => respond()))
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function withTransitions(...transitions: WarmupTransition[]) {
  respond = () => new Response(JSON.stringify({ transitions }), { status: 200, headers: jsonHeaders })
}

test('the history is loading before it arrives', () => {
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)
  expect(screen.getByText(/loading change history/i)).toBeInTheDocument()
})

// A new participant has no transitions. That is the normal state, and reading it
// as a failure would send an operator looking for a problem that isn't there.
test('a mailbox with no transitions reads as nothing having happened, not as a failure', async () => {
  withTransitions()
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  expect(await screen.findByText(/nothing has happened yet/i)).toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

test('a failed request says so and offers a retry', async () => {
  respond = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/server had a problem/i)
  // The failure must not be mistakable for an empty history.
  expect(screen.queryByText(/nothing has happened yet/i)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: /try again/i })).toBeInTheDocument()
})

// Both axes move independently and a row can move one, the other, or both, so
// each is named and shown from → to rather than collapsed into one status.
test('a row that moves both axes shows both, each with its own ends', async () => {
  withTransitions(
    transition({
      from_state: 'healthy',
      to_state: 'throttled',
      reason_code: 'campaign_bounce_throttle',
      reason: 'campaign hard-bounce rate above the throttle threshold',
      from_lane: 'healthy',
      to_lane: 'quarantine',
      lane_reason_code: 'lane_quarantined',
      lane_reason: 'quarantined: campaign hard-bounce rate above the throttle threshold',
    }),
  )
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  const entry = await screen.findByRole('listitem')
  expect(entry).toHaveTextContent(/reputation/i)
  expect(entry).toHaveTextContent(/pool/i)
  // Reputation: Healthy -> Throttled. Pool: Healthy -> Withheld.
  expect(entry).toHaveTextContent(/Healthy/)
  expect(entry).toHaveTextContent(/Throttled/)
  expect(entry).toHaveTextContent(/Withheld/)
  expect(entry).toHaveTextContent(/campaign hard bounces crossed the throttle threshold/i)
  expect(entry).toHaveTextContent(/withheld from the pool/i)
})

// Rows written before pool lanes existed carry null lane fields. History without
// a lane is a fact about the past, not an error and not a blank.
test('a pre-lane row says the lane was not recorded rather than inventing one', async () => {
  withTransitions(
    transition({
      from_state: 'unknown',
      to_state: 'watch',
      reason_code: 'spam_watch',
      reason: 'spam placement rate above the watch threshold',
      from_lane: null,
      to_lane: null,
      lane_reason_code: null,
      lane_reason: null,
    }),
  )
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  const entry = await screen.findByRole('listitem')
  expect(entry).toHaveTextContent(/no pool lane was recorded/i)
  // No lane chip at all — "Proving" here would be a fabricated fact about the past.
  expect(entry.querySelector('[data-slot="lane-badge"]')).toBeNull()
  expect(entry).not.toHaveTextContent(/proving/i)
  // …and it is not an error state.
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

test('an axis that did not move is labelled unchanged rather than shown as a move', async () => {
  withTransitions(transition({ from_lane: 'probation', to_lane: 'probation' }))
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  const entry = await screen.findByRole('listitem')
  expect(entry).toHaveTextContent(/unchanged/i)
})

// THE case this panel exists to prevent: the API reports a lower-bounded 0 for a
// sample below the policy minimum, so 3 spam placements in 5 sends arrive as
// `spam_rate: 0`. Rendering that as "0% spam" would be actively misleading.
test('a lower-bounded zero over a real sample never renders as a clean percentage', async () => {
  withTransitions(
    transition({
      placement_samples: 5,
      spam_rate: 0,
      bounce_samples: 0,
      bounce_rate: 0,
      complaint_samples: 0,
      complaint_rate: 0,
    }),
  )
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  const entry = await screen.findByRole('listitem')
  expect(entry).toHaveTextContent(/not established/i)
  expect(entry.textContent).not.toMatch(/\b0(\.0+)?\s*%/)
  // The sample size travels with the figure — a rate without one is the thing
  // the whole design exists to stop showing people.
  expect(entry).toHaveTextContent(/5 observations/)
})

test('a measured rate is shown as a bound with its sample, never as a flat rate', async () => {
  withTransitions(transition({ placement_samples: 40, spam_rate: 0.02 }))
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  const entry = await screen.findByRole('listitem')
  expect(entry).toHaveTextContent(/at least 2\.0%/)
  expect(entry).toHaveTextContent(/95% confidence lower bound over 40 observations/i)
})

// A machine token in the interface is the failure the reason map exists to
// prevent — including for a code this build has never seen.
test('no reason code reaches the screen', async () => {
  withTransitions(
    transition({ reason_code: 'spam_pause', reason: 'spam placement rate above the pause threshold' }),
    transition({ id: 't-2', reason_code: 'brand_new_code', reason: 'a policy this build has not learned' }),
  )
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  await screen.findAllByRole('listitem')
  expect(document.body.textContent).not.toContain('spam_pause')
  expect(document.body.textContent).not.toContain('brand_new_code')
  expect(screen.getByText(/a policy this build has not learned/i)).toBeInTheDocument()
})

// Which thresholds produced the decision, so an old row stays readable after the
// policy moves (acceptance criterion 4 of the phase-1 design).
test('each entry names the policy version that decided it', async () => {
  withTransitions(transition({ policy_version: 'warmup-phase1-v1' }))
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  expect(await screen.findByText(/warmup-phase1-v1/)).toBeInTheDocument()
})

test('the request asks for one mailbox history at the panel limit', async () => {
  withTransitions()
  renderWithProviders(<WarmupTransitionsPanel mailboxId="mb-1" />)

  await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalled())
  const request = vi.mocked(fetch).mock.calls[0]?.[0]
  const url = request instanceof Request ? request.url : String(request)
  expect(url).toContain('/warmup/mailboxes/mb-1/transitions')
  expect(url).toContain('limit=20')
})

import { expect, test, type Page, type Route } from '@playwright/test'

/**
 * The warmup pool surfaces in a real browser.
 *
 * The unit tests assert the copy rules against a mocked `fetch`. What they cannot
 * see is the integration this screen actually depends on: whether the lazy panel
 * fetches only when opened, whether a lower-bounded zero survives rendering as
 * "Not established" rather than a confident 0%, and — the one with history —
 * whether the lane badge reflects the lane the API returned or the SAFE DEFAULT it
 * falls back to. `/warmup/overview` once shipped without populating `lane` at all;
 * every mailbox rendered "Proving", nothing failed, and no test caught it because
 * the fallback is silent by design. A browser test is what closes that class.
 *
 * Every /api/v1 route is mocked in the browser, so no API server or database is
 * needed (same approach as deliverability.spec.ts).
 */

const WITHHELD_MAILBOX = 'mailbox-withheld'

const json = (body: unknown) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify(body),
})

/**
 * Two participants whose two axes DISAGREE, which is the whole point of the split:
 * the first is reputation-healthy but withheld from the pool, the second is the
 * reverse. A UI that collapsed the axes would render these identically.
 */
const OVERVIEW = {
  pool_size: 2,
  active: true,
  mailboxes: [
    {
      mailbox_id: WITHHELD_MAILBOX,
      email: 'withheld@acme.test',
      enabled: true,
      health_state: 'healthy',
      health_reason: '',
      lane: 'quarantine',
      lane_reason: 'quarantined: campaign hard-bounce rate above the pause threshold',
      today_sent: 0,
      today_target: 0,
      placement_sample_7d: 40,
      inbox_rate_7d: 0.9,
      spam_rate_7d: 0.1,
    },
    {
      mailbox_id: 'mailbox-proving',
      email: 'proving@acme.test',
      enabled: true,
      health_state: 'unknown',
      health_reason: 'not enough fresh placement evidence',
      lane: 'probation',
      lane_reason: 'awaiting a qualified clean window',
      today_sent: 2,
      today_target: 5,
      placement_sample_7d: 3,
      inbox_rate_7d: null,
      spam_rate_7d: null,
    },
  ],
}

/**
 * One transition carrying the two shapes most likely to be rendered dishonestly: a
 * spam rate of 0 over a REAL sample (a floor below the policy minimum, not a
 * measurement), and no recorded lane (an entry predating pool lanes).
 */
const TRANSITIONS = {
  transitions: [
    {
      id: 'transition-1',
      created_at: '2026-08-13T09:30:00Z',
      from_state: 'healthy',
      to_state: 'watch',
      reason_code: 'campaign_bounce_watch',
      reason: 'campaign hard-bounce rate above the watch threshold',
      from_lane: null,
      to_lane: null,
      lane_reason_code: null,
      lane_reason: null,
      placement_samples: 5,
      spam_rate: 0,
      bounce_population: 'campaign',
      bounce_samples: 200,
      bounce_rate: 0.061,
      complaint_samples: 0,
      complaint_rate: 0,
      invalid_tokens: 0,
      policy_version: 'warmup-phase1-v1',
    },
  ],
}

/** Counts how many times the transitions endpoint was actually hit. */
let transitionRequests = 0

async function mockApi(page: Page) {
  transitionRequests = 0
  await page.route('**/api/v1/**', async (route: Route) => {
    const path = new URL(route.request().url()).pathname

    if (path.endsWith('/auth/login') || path.endsWith('/auth/refresh')) {
      return route.fulfill(json({
        access_token: 'browser-test-token',
        expires_in: 900,
        user_id: 'user-e2e',
        active_workspace_id: 'workspace-e2e',
        role: 'owner',
        memberships: [{ workspace_id: 'workspace-e2e', workspace_name: 'Atlas Labs', role: 'owner' }],
      }))
    }
    if (path.endsWith('/auth/me')) {
      return route.fulfill(json({
        user_id: 'user-e2e',
        active_workspace_id: 'workspace-e2e',
        role: 'owner',
        memberships: [{ workspace_id: 'workspace-e2e', workspace_name: 'Atlas Labs', role: 'owner' }],
        email_verified: true,
      }))
    }

    if (path.includes('/transitions')) {
      transitionRequests += 1
      return route.fulfill(json(TRANSITIONS))
    }
    if (path.endsWith('/warmup/overview')) return route.fulfill(json(OVERVIEW))

    if (path.endsWith('/pulse')) {
      return route.fulfill(json({
        mailboxes: { total: 2, active: 2, paused: 0, error: 0 },
        warmup: { pool: 2, unknown: 1, healthy: 0, watch: 0, at_risk: 0, probation: 1, quarantine: 1 },
        campaigns: { total: 0, running: 0, draft: 0, paused: 0 },
        contacts: { total: 0 },
        sending: { sent_today: 0, daily_cap: 100 },
        inbox: { unread: 0, interested: 0 },
        attention: [],
      }))
    }
    // The cards come from the MAILBOX list, joined to the overview entries by id —
    // the overview alone only feeds the header counts, which is why an empty list
    // here renders "No mailboxes to warm" over a pool of two.
    if (path.endsWith('/mailboxes')) {
      return route.fulfill(json(OVERVIEW.mailboxes.map((m) => ({
        id: m.mailbox_id,
        email: m.email,
        provider: 'smtp',
        status: 'active',
        warmup_enabled: true,
      }))))
    }
    if (path.endsWith('/campaigns')) return route.fulfill(json([]))

    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"unhandled e2e route"}' })
  })
}

/**
 * Addressed through the accessibility tree rather than test-only markup: the card
 * is a list item and the disclosure already carries an aria-label naming its
 * mailbox, so the test exercises the same handles a screen reader would.
 */
function card(page: Page, email: string) {
  return page.getByRole('listitem').filter({ hasText: email }).first()
}

async function openHistory(page: Page, email: string) {
  await page.getByRole('button', { name: `Change history for ${email}` }).click()
}

/** The disclosure's own region, resolved via the button's aria-controls. */
function historyPanel(page: Page) {
  return page.getByText('Change history', { exact: true }).locator('..')
}

async function signIn(page: Page) {
  await page.goto('/')
  await page.getByLabel('Email').fill('demo@inroad.test')
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill('correct-horse-battery-staple')
  await page.getByRole('button', { name: 'Log in' }).click()
}

test.beforeEach(async ({ page }) => {
  await mockApi(page)
  await signIn(page)
  await page.goto('/app/warmup')
})

// The regression that motivated this file: the lane came back absent, every card
// fell back to "probation", and the screen looked entirely plausible.
test('the lane badge shows the lane the API returned, not the safe default', async ({ page }) => {
  const withheld = card(page, 'withheld@acme.test')
  await expect(withheld).toContainText(/withheld|quarantine/i)
  await expect(withheld).not.toContainText(/proving/i)

  // And the two axes stay distinguishable: this mailbox is reputation-healthy AND
  // withheld, which a UI that collapsed them could not show.
  await expect(withheld).toContainText(/healthy/i)
})

test('the history panel does not fetch until it is opened', async ({ page }) => {
  await expect(card(page, 'withheld@acme.test')).toBeVisible()
  expect(transitionRequests).toBe(0)

  await openHistory(page, 'withheld@acme.test')

  await expect.poll(() => transitionRequests).toBe(1)
})

// A rate of 0 over a real sample is a floor, not a measurement. Rendering it as a
// clean 0% would tell an operator the mailbox is spotless on the strength of
// evidence the policy already refused to act on.
test('a lower-bounded zero renders as unestablished, never as a clean percentage', async ({ page }) => {
  await openHistory(page, 'withheld@acme.test')

  const panel = historyPanel(page)
  await expect(panel).toBeVisible()
  await expect(panel).toContainText(/not established/i)
  await expect(panel).not.toContainText(/\b0(\.0+)?\s*%/)
})

test('an entry predating pool lanes says so instead of inventing a lane', async ({ page }) => {
  await openHistory(page, 'withheld@acme.test')

  const panel = historyPanel(page)
  await expect(panel).toContainText(/predates pool lanes|no pool lane/i)
  // The bounce figure is attributed, so the operator can tell whose mail it counted.
  await expect(panel).toContainText(/campaign/i)
})

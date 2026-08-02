import { expect, test, type Page, type Route } from '@playwright/test'

/**
 * The sender pool round trip, end to end.
 *
 * `PUT /campaigns/{id}/senders` is a FULL REPLACE, so a client-side bug that drops
 * a mailbox from the payload silently shrinks the pool a campaign sends from — and
 * the mocked-fetch unit tests cannot catch a fault in the RTK Query tag wiring that
 * would leave the panel showing a stale pool after a save. This spec drives the
 * real browser through the whole loop and asserts on the request that actually went
 * out plus the state the panel settles on afterwards.
 *
 * Every /api/v1 route is mocked in the browser, so no API server or database is
 * needed (same approach as app-shell.spec.ts).
 */

const CAMPAIGN_ID = 'campaign-1'

const json = (body: unknown) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify(body),
})

const MAILBOXES = [
  { id: 'mb-1', email: 'founder@atlas.test', display_name: 'Atlas Founder', provider: 'gmail', status: 'active', daily_cap: 75 },
  { id: 'mb-2', email: 'growth@atlas.test', display_name: 'Atlas Growth', provider: 'microsoft', status: 'active', daily_cap: 50 },
]

/**
 * The read-only health/capacity half of a sender row, per mailbox. mb-1 is
 * `watch` (sending at a lowered cap) and mb-2 is `paused` by warmup and sending
 * nothing, so both branches of the display are exercised in a real browser.
 */
const HEALTH: Record<string, Record<string, unknown>> = {
  'mb-1': { health_state: 'watch', sending: true, cap_today: 52, sent_today: 12 },
  'mb-2': { health_state: 'paused', sending: false, cap_today: 0, sent_today: 0 },
}

/** The pool starts with one mailbox, so the spec can add the second one. */
const initialPool = () => ({
  rotation_mode: 'weighted',
  senders: [
    {
      mailbox_id: 'mb-1',
      email: 'founder@atlas.test',
      provider: 'gmail',
      status: 'active',
      weight: 1,
      enabled: true,
      assigned_count: 12,
      last_assigned_at: '2026-07-30T10:00:00Z',
      ...HEALTH['mb-1'],
    },
  ],
})

type SenderPayload = { rotation_mode: string; senders: { mailbox_id: string; weight?: number; enabled?: boolean }[] }

/** The row a GET would return for a saved sender, read-only fields included. */
function poolRow(sender: SenderPayload['senders'][number]) {
  const mailbox = MAILBOXES.find((m) => m.id === sender.mailbox_id)
  return {
    mailbox_id: sender.mailbox_id,
    email: mailbox?.email ?? 'unknown@atlas.test',
    provider: mailbox?.provider ?? 'smtp',
    status: 'active',
    weight: sender.weight ?? 1,
    enabled: sender.enabled ?? true,
    assigned_count: sender.mailbox_id === 'mb-1' ? 12 : 0,
    last_assigned_at: sender.mailbox_id === 'mb-1' ? '2026-07-30T10:00:00Z' : null,
    ...HEALTH[sender.mailbox_id],
  }
}

/**
 * Mocks the campaign-detail surface. The senders endpoint is stateful: a PUT
 * stores what it was sent and subsequent GETs return it, so "did the panel refetch
 * and render the saved pool" is observable rather than assumed.
 */
async function mockApi(page: Page): Promise<{ puts: SenderPayload[] }> {
  const puts: SenderPayload[] = []
  let pool: unknown = initialPool()

  await page.route('**/api/v1/**', async (route: Route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname

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

    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/senders`)) {
      if (request.method() === 'PUT') {
        const sent = request.postDataJSON() as SenderPayload
        puts.push(sent)
        // Echo the saved pool back in the response shape a GET would return, so the
        // panel's post-save state comes from the server rather than local state.
        pool = {
          rotation_mode: sent.rotation_mode,
          senders: sent.senders.map(poolRow),
        }
        return route.fulfill(json(pool))
      }
      return route.fulfill(json(pool))
    }

    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/schedule`)) {
      return route.fulfill(json({
        timezone: 'UTC',
        days: [{ weekday: 1, intervals: [{ start_minute: 540, end_minute: 1020 }] }],
        daily_limit: 250,
        preview: ['Mon 09:14:37'],
      }))
    }
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/steps`)) {
      return route.fulfill(json([
        { id: 'step-1', step_order: 1, delay_seconds: 0, subject: 'A quick idea', body_text: 'hello', body_html: '' },
      ]))
    }
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/enrollments`) || path.includes('/enrollments?')) {
      return route.fulfill(json([]))
    }
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}`)) {
      return route.fulfill(json({
        id: CAMPAIGN_ID,
        name: 'Founder signal',
        subject: 'A quick idea for {{company}}',
        status: 'running',
        tracking_enabled: true,
        stats: { sent: 124 },
        metrics: { sent: 124 },
      }))
    }
    if (path.endsWith('/campaigns')) {
      return route.fulfill(json([
        { id: CAMPAIGN_ID, name: 'Founder signal', subject: 'A quick idea', status: 'running', stats: { sent: 124 } },
      ]))
    }
    if (path.endsWith('/mailboxes')) return route.fulfill(json(MAILBOXES))
    if (path.endsWith('/lists')) return route.fulfill(json([{ id: 'list-1', name: 'SaaS founders' }]))
    // `GET /contacts` answers with a keyset page, not a bare array.
    if (path.includes('/contacts')) {
      return route.fulfill(json({ items: [], next_cursor: null, prev_cursor: null, total: 0, total_is_capped: false }))
    }

    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"unhandled e2e route"}' })
  })

  return { puts }
}

async function signIn(page: Page) {
  await page.goto('/')
  await page.getByLabel('Email').fill('demo@inroad.test')
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill('correct-horse-battery-staple')
  await page.getByRole('button', { name: 'Log in' }).click()
  await expect(page.getByRole('heading', { name: 'Your outreach command center.' })).toBeVisible()
}

test('adding a mailbox to the pool sends the whole pool and the panel reflects what was saved', async ({ page }) => {
  const { puts } = await mockApi(page)
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}`)

  // The saved pool renders, including its rotation state.
  await expect(page.getByLabel('Rotation mode')).toHaveValue('weighted')
  await expect(page.getByLabel('Include founder@atlas.test in the pool')).toBeChecked()
  await expect(page.getByLabel('Include growth@atlas.test in the pool')).not.toBeChecked()

  // Nothing to save until something changes.
  await expect(page.getByRole('button', { name: 'Save senders' })).toBeHidden()

  await page.getByLabel('Include growth@atlas.test in the pool').check()
  await page.getByLabel('Rotation mode').selectOption('round_robin')
  await page.getByRole('button', { name: 'Save senders' }).click()

  // The request carries BOTH mailboxes: a full replace that dropped one would
  // silently shrink the pool, which is the regression this spec exists for.
  await expect.poll(() => puts.length).toBe(1)
  const sent = puts[0]!
  expect(sent.rotation_mode).toBe('round_robin')
  expect(sent.senders.map((s) => s.mailbox_id).sort()).toEqual(['mb-1', 'mb-2'])

  // And the panel settles on server state — proving the tag invalidation refetched
  // rather than the UI just showing optimistic local edits.
  await expect(page.getByRole('button', { name: 'Save senders' })).toBeHidden()
  await expect(page.getByLabel('Rotation mode')).toHaveValue('round_robin')
  await expect(page.getByLabel('Include growth@atlas.test in the pool')).toBeChecked()

  // The mailbox just added is one warmup has paused, and the row says so rather
  // than leaving the operator to wonder why the campaign is sending slowly.
  await expect(page.getByText('Paused by warmup — not sending')).toBeVisible()
  await expect(page.getByText('0 / 0 sent today')).toBeVisible()
})

test('a pool row accounts for the volume it has sent and for a warmup-lowered cap', async ({ page }) => {
  await mockApi(page)
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}`)

  await expect(page.getByText('12 / 52 sent today')).toBeVisible()
  await expect(page.getByText(/Cap lowered by warmup health/)).toBeVisible()
  // Still sending, so it must not carry the gated copy.
  await expect(page.getByText(/not sending/)).toBeHidden()

  // The campaign-wide limit is on the same surface, and says what it applies to.
  await expect(page.getByLabel('Daily limit')).toHaveValue('250')
  await expect(page.getByText(/not per mailbox/)).toBeVisible()
})

test('excluding every mailbox is refused client-side without a request', async ({ page }) => {
  const { puts } = await mockApi(page)
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}`)

  await page.getByLabel('Include founder@atlas.test in the pool').uncheck()
  await page.getByRole('button', { name: 'Save senders' }).click()

  await expect(page.getByRole('alert')).toContainText(/at least one/i)
  // An empty pool must never reach the API: it would park every new contact.
  expect(puts).toHaveLength(0)
})

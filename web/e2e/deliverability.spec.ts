import { expect, test, type Page, type Route } from '@playwright/test'

/**
 * The deliverability surfaces in a real browser.
 *
 * The unit tests assert the copy rules against a mocked `fetch`; what they cannot
 * see is whether the honest version of that copy actually survives rendering — a
 * "not measured" component that ships behind an overflow clip, a provisional score
 * whose qualifying sentence is pushed off a narrow viewport, or a pause explanation
 * that only exists in the DOM. Those are the failures that would put an operator
 * back in front of an unexplained number, so they get a browser test.
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

/** A day of the series. Complaints are absent throughout — the v1 reality. */
const day = (date: string, delivered: number, bounced: number, spamPlaced: number) => ({
  date,
  delivered,
  bounced,
  complained: null,
  spam_placed: spamPlaced,
})

/**
 * A low-confidence report: 96 points over eleven delivered emails. Deliberately
 * the most misleading shape this screen can be handed.
 */
const REPORT = {
  score: {
    value: 96,
    confidence: 'low',
    delivered: 11,
    components: [
      { key: 'bounce', label: 'Bounces', penalty: 4, rate: 1.2, measured: true },
      { key: 'complaint', label: 'Complaints', penalty: 0, rate: null, measured: false },
      { key: 'spam_placement', label: 'Spam placement', penalty: 0, rate: 0, measured: true },
      { key: 'warmup', label: 'Warmup', penalty: 0, rate: null, measured: true, detail: 'Every warming mailbox is healthy.' },
    ],
  },
  series: [day('2026-08-18', 4, 0, 0), day('2026-08-19', 5, 1, 1), day('2026-08-20', 2, 0, 0)],
  at_risk_mailboxes: [{ label: 'growth@atlas.test', reason: 'Bounce rate 11.4% over 640 delivered.' }],
  at_risk_domains: [],
}

/** A campaign the breaker has already stopped, with the pause it recorded. */
const CAMPAIGN_DELIVERABILITY = {
  verdict: 'paused',
  guardrails: { auto_pause_enabled: true, bounce_pause_pct: 8, complaint_pause_pct: 1.5 },
  pause_events: [
    {
      reason: 'bounce_spike',
      metric: 'bounce_rate',
      value: 9.2,
      threshold: 8,
      delivered: 218,
      created_at: '2026-08-12T04:11:00Z',
    },
  ],
  score: { value: 48, confidence: 'high', delivered: 218, components: [] },
}

async function mockApi(page: Page) {
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

    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/deliverability`)) {
      return route.fulfill(json(CAMPAIGN_DELIVERABILITY))
    }
    if (path.endsWith('/deliverability')) return route.fulfill(json(REPORT))

    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/senders`)) {
      return route.fulfill(json({ rotation_mode: 'weighted', senders: [] }))
    }
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/schedule`)) {
      return route.fulfill(json({ timezone: 'UTC', days: [], daily_limit: null, preview: [] }))
    }
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/steps`)) return route.fulfill(json([]))
    if (path.includes('/enrollments')) return route.fulfill(json([]))
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}`)) {
      return route.fulfill(json({
        id: CAMPAIGN_ID,
        name: 'Founder signal',
        subject: 'A quick idea for {{company}}',
        status: 'paused',
        tracking_enabled: true,
        stats: { sent: 218 },
        metrics: { sent: 218 },
      }))
    }
    if (path.endsWith('/campaigns')) {
      return route.fulfill(json([
        { id: CAMPAIGN_ID, name: 'Founder signal', subject: 'A quick idea', status: 'paused', stats: { sent: 218 } },
      ]))
    }
    if (path.endsWith('/mailboxes')) {
      return route.fulfill(json([
        { id: 'mb-1', email: 'founder@atlas.test', provider: 'gmail', status: 'active', daily_cap: 75 },
      ]))
    }
    if (path.endsWith('/warmup/overview')) {
      return route.fulfill(json({ pool_size: 1, active: false, mailboxes: [] }))
    }
    if (path.endsWith('/lists')) return route.fulfill(json([]))
    if (path.includes('/contacts')) {
      return route.fulfill(json({ items: [], next_cursor: null, prev_cursor: null, total: 0, total_is_capped: false }))
    }

    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"unhandled e2e route"}' })
  })
}

async function signIn(page: Page) {
  await page.goto('/')
  await page.getByLabel('Email').fill('demo@inroad.test')
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill('correct-horse-battery-staple')
  await page.getByRole('button', { name: 'Log in' }).click()
  await expect(page.getByRole('heading', { name: 'Your outreach command center.' })).toBeVisible()
}

test('the deliverability page qualifies a small-sample score and never shows an unmeasured signal as clean', async ({
  page,
}, testInfo) => {
  await mockApi(page)
  await signIn(page)

  await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: /Deliverability/ }).click()

  const score = page.getByRole('region', { name: 'Deliverability score' })
  await expect(score).toContainText('96/100')
  // 96 over eleven delivered is not a clean bill of health, and the page says so
  // in a sentence rather than a badge.
  await expect(score.getByText('Provisional', { exact: true })).toBeVisible()
  await expect(score.getByText(/11 delivered — too small a sample to be a verdict/)).toBeVisible()
  await expect(score.getByText('Strong')).toBeHidden()

  // The complaint component has no feed behind it. "Not measured", never 0%.
  const complaints = score.locator('[data-component="complaint"]')
  await expect(complaints.getByText('Not measured')).toBeVisible()
  await expect(complaints.getByText(/No complaint feed is connected/)).toBeVisible()
  await expect(complaints).not.toContainText('0.0%')

  await page.screenshot({ path: testInfo.outputPath('deliverability-score.png') })

  // The per-day chart loads its own chunk, and the unmeasured signal is a
  // sentence there too rather than a flat line along zero.
  await expect(page.getByRole('region', { name: 'Bounce rate' })).toBeVisible()
  const complaintPanel = page.getByRole('region', { name: 'Complaint rate' })
  await expect(complaintPanel.getByText(/not a run of clean days/)).toBeVisible()
  await expect(complaintPanel.locator('svg')).toHaveCount(0)

  // Every plotted value is also reachable as text.
  await page.getByText('Show these days as a table').click()
  await expect(page.getByRole('table', { name: /Deliverability signals per day/ })).toBeVisible()

  await expect(
    page.getByRole('region', { name: 'Mailboxes at risk' }).getByText('Bounce rate 11.4% over 640 delivered.'),
  ).toBeVisible()

  await page.screenshot({ path: testInfo.outputPath('deliverability-light.png'), fullPage: true })

  // The same screen in dark mode: tokens, not hardcoded greys.
  await page.getByRole('button', { name: 'Use dark theme' }).click()
  await expect(page.locator('html')).toHaveClass(/dark/)
  await expect(score.getByText('Provisional', { exact: true })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('deliverability-dark.png'), fullPage: true })
})

test('an automatically paused campaign explains itself on a phone-sized viewport', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockApi(page)
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}/preferences`)

  const card = page.getByRole('region', { name: 'Deliverability guardrails' })
  await expect(card.getByText('Paused by the guardrail')).toBeVisible()
  // The whole point: reason, observed rate, threshold and sample, all visible.
  await expect(
    card.getByText('Paused automatically on 12 Aug — bounce rate 9.2% over 218 delivered, threshold 8.0%.'),
  ).toBeVisible()
  await expect(card.getByText('Bounce spike')).toBeVisible()
  await expect(card.getByLabel('Bounce threshold')).toHaveValue('8')
  await expect(card.getByLabel('Complaint threshold')).toHaveValue('1.5')
  await expect(card.getByRole('switch', { name: 'Turn automatic pausing off' })).toBeVisible()

  // Long explanatory copy is the most likely thing to blow out a narrow layout.
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false)
  await card.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('guardrails-mobile.png') })
})

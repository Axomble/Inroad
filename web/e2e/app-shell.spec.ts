import { expect, test, type Page } from '@playwright/test'

const json = (body: unknown) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify(body),
})

async function mockApi(page: Page) {
  await page.route('**/api/v1/**', async (route) => {
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
    // The overview's tiles read this one endpoint. They used to sum the mailbox
    // list client-side — which is where the 125 the test asserts came from, 75 + 50
    // — and #157 replaced that with the pulse. This route was never added, so every
    // tile rendered a dash and the assertion broke. daily_cap stays 125 so the
    // assertion keeps meaning "the overview shows the workspace's daily capacity"
    // rather than being retargeted at whatever the new page happens to render.
    if (path.endsWith('/pulse')) {
      return route.fulfill(json({
        mailboxes: { total: 2, active: 2, paused: 0, error: 0 },
        warmup: { pool: 2, unknown: 0, healthy: 1, watch: 1, at_risk: 0, probation: 0, quarantine: 0 },
        campaigns: { total: 1, running: 1, draft: 0, paused: 0 },
        contacts: { total: 3 },
        sending: { sent_today: 7, daily_cap: 125 },
        inbox: { unread: 1, interested: 1 },
        // The degrading mailbox's reason used to reach this page through the warmup
        // overview's health_reason. The pulse now owns the priority queue, so the
        // same fact arrives as a server-defined attention row — which is what the
        // assertion below reads, and what the page actually renders.
        attention: [
          {
            kind: 'warmup_watch',
            severity: 'warn',
            count: 1,
            reason: 'Inbox placement is trending down.',
            href: '/app/warmup',
          },
        ],
      }))
    }
    if (path.endsWith('/mailboxes')) {
      return route.fulfill(json([
        { id: 'mb-1', email: 'founder@atlas.test', display_name: 'Atlas Founder', provider: 'gmail', status: 'active', daily_cap: 75 },
        { id: 'mb-2', email: 'growth@atlas.test', display_name: 'Atlas Growth', provider: 'microsoft', status: 'active', daily_cap: 50 },
      ]))
    }
    // The Mailboxes page's domain-authentication panel. `p=none` + undetected
    // DKIM is the shape most at risk of being mis-worded, so that's what the
    // browser test renders.
    if (path.includes('/sending-domains')) {
      return route.fulfill(json([
        {
          domain: 'atlas.test',
          state: 'passing',
          spf: { found: true, record: 'v=spf1 include:_spf.google.com ~all' },
          dmarc: { found: true, policy: 'none' },
          dkim: { found: false },
          mailbox_count: 2,
          checked_at: new Date(Date.now() - 3_600_000).toISOString(),
        },
      ]))
    }
    if (path.endsWith('/warmup/overview')) {
      return route.fulfill(json({
        pool_size: 2,
        active: true,
        mailboxes: [
          { mailbox_id: 'mb-1', email: 'founder@atlas.test', enabled: true, health_state: 'healthy', health_reason: '', today_sent: 4, today_target: 6, inbox_rate_7d: 0.96, spam_rate_7d: 0.01 },
          { mailbox_id: 'mb-2', email: 'growth@atlas.test', enabled: true, health_state: 'watch', health_reason: 'Inbox placement is trending down.', today_sent: 3, today_target: 5, inbox_rate_7d: 0.78, spam_rate_7d: 0.12 },
        ],
      }))
    }
    if (path.endsWith('/campaigns')) {
      return route.fulfill(json([
        { id: 'campaign-1', name: 'Founder signal', subject: 'A quick idea for {{company}}', status: 'running', stats: { sent: 124 } },
        { id: 'campaign-2', name: 'Agency partners', subject: 'Partner with Atlas', status: 'draft', stats: { sent: 0 } },
      ]))
    }
    if (path.endsWith('/lists')) return route.fulfill(json([{ id: 'list-1', name: 'SaaS founders' }]))
    // `GET /contacts` answers with a keyset page, not a bare array.
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

test('operator can navigate, inspect live metrics, search commands, and switch theme', async ({ page }, testInfo) => {
  await mockApi(page)
  await signIn(page)

  // Scoped to the tile rather than a bare getByText('125'): the pulse's daily cap
  // now appears in more than one place on this page, and an unscoped match is a
  // strict-mode violation. Naming the tile also states what is being checked — the
  // overview reports the workspace's daily capacity — instead of asserting that the
  // digits exist somewhere.
  await expect(page.locator('article', { hasText: 'Daily capacity' })).toContainText('125')
  await expect(page.getByText('Founder signal')).toBeVisible()
  // Scoped for the same reason as the tile above: the reason string reaches the DOM
  // in more than one place. Reading it inside the priority queue is also the
  // stronger claim — it asserts the degrading mailbox is SURFACED for triage, not
  // merely that the sentence exists somewhere on the page.
  await expect(page.locator('section', { hasText: 'Needs attention' })).toContainText(
    'Inbox placement is trending down.',
  )

  await page.getByRole('button', { name: 'Open command palette' }).click()
  await page.getByRole('searchbox', { name: 'Search commands' }).fill('warm')
  await expect(page.getByRole('dialog', { name: 'Command palette' }).getByText('Warmup')).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('button', { name: 'Use dark theme' }).click()
  await expect(page.locator('html')).toHaveClass(/dark/)
  await page.screenshot({ path: testInfo.outputPath('overview-desktop.png'), fullPage: true })
})

test('mobile navigation and core screens stay within the viewport', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockApi(page)
  await signIn(page)

  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false)
  await page.getByRole('button', { name: 'Toggle navigation' }).click()
  const drawer = page.getByRole('dialog', { name: 'Primary navigation' })
  await expect(drawer).toBeVisible()
  await drawer.getByRole('link', { name: /Mailboxes/ }).click()
  await expect(page.getByText('founder@atlas.test')).toBeVisible()

  // Domain authentication renders on the narrow viewport with its nuances
  // intact: DMARC published but not enforcing, DKIM undetected rather than
  // broken. Long DNS guidance is the most likely thing to blow the layout out.
  //
  // It is now a per-domain header above that domain's own mailboxes, not one
  // panel above the whole list, so there is no 'Domain authentication' region to
  // scope to. On a narrow viewport the per-record pills are deliberately hidden
  // (`hidden md:flex` in DomainAuthHeader) — the domain-level verdict plus the
  // disclosure carry the same answer without wrapping the header onto a second
  // line. So the nuance is asserted where a phone user actually reads it: behind
  // the disclosure, which is also where the long guidance that threatens the
  // layout lives.
  await expect(page.getByText('atlas.test', { exact: true })).toBeVisible()
  await expect(page.getByText('Authenticated')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Recheck DNS for atlas.test' })).toBeVisible()

  await page.getByRole('button', { name: 'Show DNS records for atlas.test' }).click()
  await expect(page.getByText('Monitoring only.')).toBeVisible()
  await expect(page.getByText('Not detected.')).toBeVisible()

  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false)
  await page.screenshot({ path: testInfo.outputPath('overview-mobile.png'), fullPage: true })
})

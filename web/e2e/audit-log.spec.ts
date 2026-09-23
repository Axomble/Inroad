import { expect, test, type Page, type Route } from '@playwright/test'

/**
 * The audit log in a real browser.
 *
 * The unit suite drives the filters and the cursor rules against a mocked
 * `fetch` and a mocked router. What it cannot see is whether the real route is
 * registered and lazily loaded, whether the settings rail actually offers it to an
 * owner, whether `validateSearch` keeps the filters in a real address bar, and
 * whether hostile strings in a recorded event survive rendering as inert text.
 *
 * Every /api/v1 route is mocked in the browser, so no API server or database is
 * needed (same approach as deliverability.spec.ts).
 */

const json = (body: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })

const SESSION = {
  user_id: 'user-e2e',
  active_workspace_id: 'workspace-e2e',
  role: 'owner',
  memberships: [{ workspace_id: 'workspace-e2e', workspace_name: 'Atlas Labs', role: 'owner' }],
}

const HOSTILE_UA = '<img src=x onerror="window.xssProbe=1"> Mozilla/5.0'

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString()

const FIRST_PAGE = {
  events: [
    {
      id: 'ev-1',
      action: 'auth.login_failed',
      actor_type: 'user',
      actor_id: null,
      actor_user_id: null,
      actor_email: null,
      target_type: null,
      target_id: null,
      ip: '198.51.100.23',
      user_agent: HOSTILE_UA,
      metadata: { email: 'ceo@atlas.test', reason: 'bad_password' },
      created_at: minutesAgo(5),
    },
    {
      id: 'ev-2',
      action: 'member.role_changed',
      actor_type: 'user',
      actor_id: 'user-e2e',
      actor_user_id: 'user-e2e',
      actor_email: 'owner@atlas.test',
      target_type: 'member',
      target_id: 'user-2',
      ip: '203.0.113.7',
      user_agent: 'Mozilla/5.0',
      metadata: { from_role: 'member', to_role: 'admin' },
      created_at: minutesAgo(40),
    },
  ],
  next_cursor: 'cursor-2',
}

const SECOND_PAGE = {
  events: [
    {
      id: 'ev-3',
      action: 'apikey.created',
      actor_type: 'api_key',
      actor_id: 'key-7',
      actor_user_id: 'user-e2e',
      actor_email: 'owner@atlas.test',
      target_type: 'api_key',
      target_id: 'key-8',
      ip: '203.0.113.7',
      user_agent: 'curl/8.4',
      metadata: { name: 'CI deploys' },
      created_at: minutesAgo(600),
    },
  ],
  next_cursor: null,
}

/** Every audit request the page sent, so the filter → request mapping is assertable. */
async function mockApi(page: Page, auditRequests: URLSearchParams[]) {
  await page.route('**/api/v1/**', async (route: Route) => {
    const url = new URL(route.request().url())
    const path = url.pathname

    if (path.endsWith('/auth/login') || path.endsWith('/auth/refresh')) {
      return route.fulfill(json({ access_token: 'browser-test-token', expires_in: 900, ...SESSION }))
    }
    if (path.endsWith('/auth/me')) return route.fulfill(json({ ...SESSION, email_verified: true }))

    if (path.endsWith('/audit-events')) {
      auditRequests.push(url.searchParams)
      if (url.searchParams.get('cursor') === 'cursor-2') return route.fulfill(json(SECOND_PAGE))
      if (url.searchParams.get('action') === 'apikey') return route.fulfill(json(SECOND_PAGE))
      return route.fulfill(json(FIRST_PAGE))
    }

    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"unhandled e2e route"}' })
  })
}

async function signIn(page: Page) {
  await page.goto('/')
  await page.getByLabel('Email').fill('demo@inroad.test')
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill('correct-horse-battery-staple')
  await page.getByRole('button', { name: 'Log in' }).click()
  await page.waitForURL(/\/app$/)
  await expect(page.getByRole('main')).toBeVisible()
}

test('an owner reads, expands, pages and filters the audit log', async ({ page }, testInfo) => {
  const auditRequests: URLSearchParams[] = []
  await mockApi(page, auditRequests)
  await signIn(page)

  // Reached the way an owner reaches it: through the settings rail.
  await page.goto('/app/settings/security')
  await page.getByRole('navigation', { name: 'Settings' }).getByRole('link', { name: 'Audit log' }).click()
  await page.waitForURL(/\/app\/settings\/audit-log$/)

  const feed = page.locator('[data-slot="audit-feed"]')
  const failed = feed.locator('li', { hasText: 'Unknown (failed sign-in)' })
  await expect(failed).toContainText('Sign-in failed')
  await expect(failed).toContainText('tried ceo@atlas.test')
  await expect(feed.locator('li', { hasText: 'Role changed' })).toContainText('owner@atlas.test')

  // Expanding shows the recorded detail — and the hostile user agent as text.
  const toggle = failed.getByRole('button')
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  const details = failed.locator('[data-slot="audit-event-details"]')
  await expect(details).toContainText('bad_password')
  await expect(details.getByText(HOSTILE_UA)).toBeVisible()
  await expect(page.locator('[data-slot="audit-feed"] img')).toHaveCount(0)
  expect(await page.evaluate(() => (window as unknown as { xssProbe?: number }).xssProbe)).toBeUndefined()

  await page.screenshot({ path: testInfo.outputPath('audit-log-expanded.png'), fullPage: true })

  // Load more appends the page the cursor names, then says the log is exhausted.
  await feed.getByRole('button', { name: 'Load older events' }).click()
  await expect(feed.locator('li', { hasText: 'API key created' })).toContainText('API key key-7')
  await expect(feed.getByText('That is everything for these filters.')).toBeVisible()
  expect(auditRequests.at(-1)?.get('cursor')).toBe('cursor-2')

  // A category filter goes to the URL and to the API — with no cursor carried over.
  await page.getByLabel('Action').selectOption('apikey')
  await page.waitForURL(/[?&]action=apikey/)
  await expect(feed.locator('li', { hasText: 'Unknown (failed sign-in)' })).toHaveCount(0)
  await expect(feed.locator('li', { hasText: 'API key created' })).toHaveCount(1)
  const filtered = auditRequests.at(-1)
  expect(filtered?.get('action')).toBe('apikey')
  expect(filtered?.has('cursor')).toBe(false)

  // The filter survives a reload, because it lives in the address bar.
  await page.reload()
  await expect(page.getByLabel('Action')).toHaveValue('apikey')

  await page.getByRole('button', { name: 'Use dark theme' }).click()
  await expect(page.locator('html')).toHaveClass(/dark/)
  await page.screenshot({ path: testInfo.outputPath('audit-log-dark.png'), fullPage: true })
})

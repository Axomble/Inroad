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
    if (path.endsWith('/mailboxes')) {
      return route.fulfill(json([
        { id: 'mb-1', email: 'founder@atlas.test', display_name: 'Atlas Founder', provider: 'gmail', status: 'active', daily_cap: 75 },
        { id: 'mb-2', email: 'growth@atlas.test', display_name: 'Atlas Growth', provider: 'microsoft', status: 'active', daily_cap: 50 },
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
    if (path.includes('/contacts')) return route.fulfill(json([]))
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

  await expect(page.getByText('125')).toBeVisible()
  await expect(page.getByText('Founder signal')).toBeVisible()
  await expect(page.getByText('Inbox placement is trending down.')).toBeVisible()

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

  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false)
  await page.screenshot({ path: testInfo.outputPath('overview-mobile.png'), fullPage: true })
})

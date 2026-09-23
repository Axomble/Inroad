import { expect, test, type Page } from '@playwright/test'

/**
 * The inbox search box, in a real browser: type a query, see the highlighted
 * hit, open it in the reader pane. The unit tests drive the same flow against a
 * mocked router; this is the one place the real TanStack router carries `?q=`
 * through `validateSearch` and the real three-pane layout (a desktop viewport
 * matches its media query) opens the hit in place.
 *
 * Every /api/v1 route is mocked in the browser, so no API server or database is
 * needed (same approach as app-shell.spec.ts).
 */

const json = (body: unknown) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify(body),
})

const SESSION = {
  user_id: 'user-e2e',
  active_workspace_id: 'workspace-e2e',
  role: 'owner',
  memberships: [{ workspace_id: 'workspace-e2e', workspace_name: 'Atlas Labs', role: 'owner' }],
}

const THREAD = {
  id: 'thread-1',
  mailbox_id: 'mb-1',
  campaign_id: 'campaign-1',
  contact_id: 'contact-1',
  contact_email: 'jamie@prospect.test',
  contact_first_name: 'Jamie',
  contact_last_name: 'Lin',
  subject: 'A quick idea for Prospect',
  last_reply_class: 'positive',
  reply_label: null,
  labels: [],
  unread: false,
  last_message_at: new Date(Date.now() - 3_600_000).toISOString(),
}

async function mockApi(page: Page) {
  const searches: URL[] = []
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    if (path.endsWith('/auth/login') || path.endsWith('/auth/refresh')) {
      return route.fulfill(json({ access_token: 'browser-test-token', expires_in: 900, ...SESSION }))
    }
    if (path.endsWith('/auth/me')) return route.fulfill(json({ ...SESSION, email_verified: true }))
    if (path.endsWith('/pulse')) {
      return route.fulfill(json({
        mailboxes: { total: 1, active: 1, paused: 0, error: 0 },
        warmup: { pool: 1, unknown: 0, healthy: 1, watch: 0, at_risk: 0 },
        campaigns: { total: 1, running: 1, draft: 0, paused: 0 },
        contacts: { total: 1 },
        sending: { sent_today: 0, daily_cap: 50 },
        inbox: { unread: 0, interested: 1 },
        attention: [],
      }))
    }
    if (path.endsWith('/mailboxes')) {
      return route.fulfill(json([
        { id: 'mb-1', email: 'founder@atlas.test', display_name: 'Atlas Founder', provider: 'gmail', status: 'active', daily_cap: 50 },
      ]))
    }
    if (path.endsWith('/reply-labels')) return route.fulfill(json({ labels: [] }))
    if (path.endsWith('/inbox/labels')) return route.fulfill(json({ labels: [] }))
    if (path.endsWith('/inbox/overview')) {
      return route.fulfill(json({
        total: 1, unread: 0, today: 1, this_week: 1, awaiting_reply: 0, snoozed: 0,
        by_mailbox: [{ mailbox_id: 'mb-1', total: 1, unread: 0 }],
        by_reply_class: [],
      }))
    }
    if (path.endsWith('/inbox/threads')) return route.fulfill(json({ items: [THREAD] }))
    if (path.endsWith('/inbox/search')) {
      searches.push(url)
      return route.fulfill(json({
        items: [
          {
            thread: THREAD,
            matched_legs: ['inbound', 'outbound'],
            snippet: {
              direction: 'inbound',
              occurred_at: THREAD.last_message_at,
              subject: [{ text: 'Re: A quick idea for Prospect', match: false }],
              // Markup in message content must arrive on screen as text.
              body: [
                { text: 'Sounds good <b>really</b> — can we set up a ', match: false },
                { text: 'meeting', match: true },
                { text: ' on Thursday?', match: false },
              ],
            },
          },
        ],
        next_cursor: null,
      }))
    }
    if (path.endsWith(`/inbox/threads/${THREAD.id}`)) {
      return route.fulfill(json({
        ...THREAD,
        pending_reply: null,
        snooze: null,
        messages: [
          {
            direction: 'inbound',
            message_id: 'm-1',
            from_email: THREAD.contact_email,
            from_name: 'Jamie Lin',
            to_email: 'founder@atlas.test',
            subject: 'Re: A quick idea for Prospect',
            body_text: 'Sounds good — can we set up a meeting on Thursday?',
            body_html: '',
            reply_class: 'positive',
            occurred_at: THREAD.last_message_at,
          },
        ],
      }))
    }
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"unhandled e2e route"}' })
  })
  return { searches }
}

async function signIn(page: Page) {
  await page.goto('/')
  await page.getByLabel('Email').fill('demo@inroad.test')
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill('correct-horse-battery-staple')
  await page.getByRole('button', { name: 'Log in' }).click()
  await page.waitForURL(/\/app$/)
  await expect(page.getByRole('main')).toBeVisible()
}

test('searching the inbox highlights the match and opens the hit in the reader', async ({ page }) => {
  const { searches } = await mockApi(page)
  await signIn(page)
  await page.goto('/app/inbox')
  await expect(page.getByText('Jamie Lin').first()).toBeVisible()

  // `/` is the list's focus shortcut; the query lands in the URL once typing pauses.
  await page.locator('body').press('/')
  await expect(page.getByRole('searchbox', { name: 'Search mail…' })).toBeFocused()
  await page.keyboard.type('meeting')
  await page.waitForURL(/[?&]q=meeting/)

  const results = page.getByRole('list', { name: 'Search results for meeting' })
  await expect(results.locator('mark')).toHaveText('meeting')
  await expect(results).toContainText('Sounds good <b>really</b> — can we set up a meeting on Thursday?')
  await expect(results.locator('b')).toHaveCount(0)
  await expect(results).toContainText('Matched in received & sent')
  expect(searches.at(-1)?.searchParams.get('q')).toBe('meeting')

  await results.getByText('Jamie Lin').click()

  const reader = page.getByRole('region', { name: 'Thread' })
  await expect(reader.getByText('Sounds good — can we set up a meeting on Thursday?')).toBeVisible()
})

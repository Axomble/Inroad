import { expect, test, type Page } from '@playwright/test'

/**
 * The realtime socket, driven through a REAL browser WebSocket.
 *
 * These exist because of a bug that made realtime unreachable from a browser
 * entirely, and which no existing suite could catch. Two defects had to be
 * fixed before a single event reached a client: the /ws route was mounted
 * behind a Bearer-only RequireAuth (a browser cannot set headers on
 * `new WebSocket()`, so every handshake 401'd), and Vite's dev proxy dropped
 * the Upgrade because it lacked `ws: true`.
 *
 * The other specs in this directory mock `**\/api/v1/**` and the unit tests
 * inject a fake socket, so both stayed green against a completely dead
 * connection. What these add is a REAL browser WebSocket: a genuine handshake
 * and a genuine `onmessage`, so the client half of the feature — the socket
 * client, the reducer, the cache patch, the indicator — is exercised as the
 * browser actually runs it rather than through a hand-written double.
 *
 * SCOPE, stated plainly so nobody trusts these further than they go.
 * `page.routeWebSocket` intercepts INSIDE the page, which means it never
 * traverses the Vite proxy or reaches the Go server. These tests therefore
 * CANNOT catch either original defect — verified by reverting `ws: true` and
 * watching all four still pass. A route mounted behind the wrong auth group, a
 * proxy that drops the Upgrade, a broken ticket handshake: all invisible here.
 *
 * Those live on the server side and are covered there —
 * cmd/inroad/router_test.go pins the mount (including the chi duplicate-Mount
 * panic) and internal/app/realtime pins every ticket refusal. The only
 * end-to-end proof the whole chain works is driving the real stack in a real
 * browser, which is a manual step: pause a mailbox and watch the nav change.
 */

const json = (body: unknown) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify(body),
})

const WORKSPACE = 'workspace-e2e'

/** The pulse aggregate every screen's chrome reads. Mutable per test so a
 *  realtime-driven refetch can be observed changing rendered numbers. */
function pulseBody(over: { active?: number; paused?: number; sentToday?: number } = {}) {
  const { active = 2, paused = 0, sentToday = 7 } = over
  // Mirrors GET /api/v1/pulse verbatim, `attention` included: the header reads
  // `data.attention.some(...)` unguarded, so omitting it crashes the chrome
  // into its error boundary and every assertion below becomes meaningless.
  return {
    mailboxes: { total: active + paused, active, paused, error: 0 },
    warmup: { pool: 2, unknown: 0, healthy: 2, watch: 0, at_risk: 0 },
    campaigns: { total: 0, running: 0, draft: 0, paused: 0 },
    contacts: { total: 0 },
    sending: { sent_today: sentToday, daily_cap: 125 },
    inbox: { unread: 0, interested: 0 },
    attention: [],
  }
}

/**
 * Mocks the REST surface but deliberately NOT the socket: `/realtime/ws` is
 * left for the page to open for real. `pulse` reads from a mutable holder so a
 * test can change the server's answer and then prove an event caused the
 * refetch that picks it up.
 */
async function mockApi(page: Page, state: { pulse: ReturnType<typeof pulseBody> }) {
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/realtime/ws')) return route.fallback()
    if (path.endsWith('/auth/login') || path.endsWith('/auth/refresh')) {
      return route.fulfill(json({
        access_token: 'browser-test-token',
        expires_in: 900,
        user_id: 'user-e2e',
        active_workspace_id: WORKSPACE,
        role: 'owner',
        memberships: [{ workspace_id: WORKSPACE, workspace_name: 'Atlas Labs', role: 'owner' }],
      }))
    }
    if (path.endsWith('/auth/me')) {
      return route.fulfill(json({
        user_id: 'user-e2e',
        active_workspace_id: WORKSPACE,
        role: 'owner',
        memberships: [{ workspace_id: WORKSPACE, workspace_name: 'Atlas Labs', role: 'owner' }],
        email_verified: true,
      }))
    }
    if (path.endsWith('/realtime/ticket')) {
      // Shape only — the fake socket below never validates it. What matters is
      // that the app mints before dialing, which is the real handshake order.
      return route.fulfill(json({ ticket: 'e2e-ticket', expires_in: 30 }))
    }
    if (path.endsWith('/pulse')) return route.fulfill(json(state.pulse))
    // These three answer with bare ARRAYS, not wrapped objects — confirmed
    // against the running API. A wrapped shape here reads as "no data" at best
    // and crashes a `.some()` at worst.
    if (path.endsWith('/sending-domains')) return route.fulfill(json([]))
    if (path.endsWith('/mailboxes')) return route.fulfill(json([]))
    if (path.endsWith('/campaigns')) return route.fulfill(json([]))
    return route.fulfill(json({}))
  })
}

/**
 * Serves `/api/v1/realtime/ws` as a real WebSocket the page connects to, and
 * hands the test a `send` for pushing envelopes down it. Playwright's
 * routeWebSocket intercepts in-page, so no API server is needed — but the
 * browser still performs a genuine WebSocket handshake and a genuine
 * `onmessage`, which is exactly the layer the fake-socket unit tests skip.
 */
async function serveSocket(page: Page) {
  const sent: string[] = []
  let push: ((data: string) => void) | null = null
  await page.routeWebSocket('**/api/v1/realtime/ws**', (ws) => {
    push = (data: string) => ws.send(data)
    ws.onMessage((m) => sent.push(String(m)))
  })
  return {
    clientMessages: sent,
    connected: () => push !== null,
    send: (envelope: unknown) => {
      if (!push) throw new Error('socket not connected yet')
      push(JSON.stringify(envelope))
    },
  }
}

async function login(page: Page) {
  await page.goto('/')
  await page.getByPlaceholder('you@company.com').fill('demo@inroad.test')
  await page.getByPlaceholder('Enter your password').fill('demodemo')
  await page.getByRole('button', { name: 'Log in' }).click()
  await expect(page.getByRole('link', { name: /^Overview/ })).toBeVisible()
}

test('a healthy socket shows no indicator at all, and the app is usable', async ({ page }) => {
  const state = { pulse: pulseBody() }
  await mockApi(page, state)
  const socket = await serveSocket(page)
  await login(page)

  // Healthy is silent by design (spec §6): an always-on green light is chrome
  // nobody reads. So "no indicator" IS the passing assertion here — and it is
  // only meaningful because the socket below really did open.
  await expect.poll(() => socket.connected(), { timeout: 10_000 }).toBe(true)
  await expect(page.getByText('Connecting', { exact: true })).toBeHidden()
  await expect(page.getByText('Reconnecting', { exact: true })).toBeHidden()
  await expect(page.getByText('Offline', { exact: true })).toBeHidden()
})

test('an event patches the chrome without a reload', async ({ page }) => {
  const state = { pulse: pulseBody({ active: 2, paused: 0 }) }
  await mockApi(page, state)
  const socket = await serveSocket(page)
  await login(page)
  await expect.poll(() => socket.connected(), { timeout: 10_000 }).toBe(true)

  // The nav's mailbox count comes from the pulse aggregate. Matched by href
  // rather than by name: the sidebar renders twice (desktop + mobile shells),
  // so a name locator resolves to two nodes and strict mode fails.
  const mailboxes = page.locator('a[href="/app/mailboxes"]').first()
  await expect(mailboxes).toHaveText(/Mailboxes\s*2/)

  // Move the server's answer, then deliver the event that makes the client
  // re-read it. mailbox.changed is one of the five types that call
  // refetchPulse, so this asserts the wiring end to end: frame -> reducer ->
  // cache patch -> rendered text. Without the refetch the nav would keep
  // showing 2 forever.
  state.pulse = pulseBody({ active: 3, paused: 0 })
  socket.send({
    seq: 1,
    type: 'mailbox.changed',
    subject: { kind: 'mailbox', id: 'mb-1' },
    at: new Date().toISOString(),
    data: { mailbox_id: 'mb-1', status: 'active' },
  })

  await expect(mailboxes).toHaveText(/Mailboxes\s*3/)
})

test('an unknown event type is inert rather than fatal', async ({ page }) => {
  const state = { pulse: pulseBody() }
  await mockApi(page, state)
  const socket = await serveSocket(page)
  await login(page)
  await expect.poll(() => socket.connected(), { timeout: 10_000 }).toBe(true)

  // The envelope is versionless, so a server ahead of this client must not
  // break it: applyEnvelopeToCache returns false for an unhandled type and that
  // must stay a no-op, never an error boundary.
  socket.send({
    seq: 1,
    type: 'something.invented.later',
    subject: { kind: 'workspace', id: WORKSPACE },
    at: new Date().toISOString(),
    data: {},
  })

  await expect(page.getByText('Something went wrong')).toBeHidden()
  await expect(page.getByRole('link', { name: /^Overview/ })).toBeVisible()
})

test('a dropped socket says so instead of looking like a quiet workspace', async ({ page }) => {
  const state = { pulse: pulseBody() }
  await mockApi(page, state)
  // Close every connection immediately: the client should surface the retry
  // rather than sit silently, which is the exact failure the indicator exists
  // to make visible.
  await page.routeWebSocket('**/api/v1/realtime/ws**', (ws) => ws.close())
  await login(page)

  await expect(
    page.getByText(/Connecting|Reconnecting|Offline/).first(),
  ).toBeVisible({ timeout: 15_000 })
})

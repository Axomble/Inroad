import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { MailboxesPage } from './mailboxes-page'

// MailboxesPage embeds OauthCallbackBanner, which reads the route search via
// getRouteApi, and the page's own list filter/sort lives in the URL via
// `useUrlState` (useSearch + useNavigate). Stub all three — an empty search means
// no callback banner, no filter, and the default sort.
vi.mock('@tanstack/react-router', () => ({
  getRouteApi: () => ({ useSearch: () => ({}) }),
  useSearch: () => ({}),
  useNavigate: () => () => {},
}))

// Radix DropdownMenu drives open/close through pointer + keyboard events that
// jsdom doesn't fully implement. Polyfill the capture/scroll methods Radix
// touches so the menu can actually open under test — the codebase otherwise
// avoids Radix-in-jsdom, but this interaction (close-on-error vs stay-open) is
// exactly the regression we need to lock, so we exercise the real component.
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }
const AUTH_URL = 'https://accounts.google.com/o/oauth2/v2/auth?client_id=x'
const MS_AUTH_URL = 'https://login.microsoftonline.com/common/oauth2/v2.0/authorize?client_id=y'
const ORIGINAL_LOCATION = window.location

// Per-test responders for the endpoints MailboxesPage hits.
let listResponder: () => Response
let startGoogleResponder: () => Response
let startMicrosoftResponder: () => Response
let authMeResponder: () => Response
let overviewResponder: () => Response
let warmupWriteResponder: () => Response
let connectResponder: () => Response
let requests: Array<{ method: string; url: string }>
let assignMock: ReturnType<typeof vi.fn>

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

/** A warmup overview row for a mailbox that is enrolled and healthy. */
function warmupEntry(mailboxId: string, email: string) {
  return {
    mailbox_id: mailboxId,
    email,
    enabled: true,
    health_state: 'healthy',
    health_reason: '',
    today_sent: 0,
    today_target: 13,
    inbox_rate_7d: 1,
    spam_rate_7d: 0,
  }
}

/** A signed-in session, so the page's `useEmailVerified` queries /auth/me. */
const AUTHED = { auth: { status: 'authed' as const, accessToken: 'token' } }

beforeEach(() => {
  listResponder = () =>
    new Response(
      JSON.stringify([{ id: 'm-1', email: 'sender@gmail.com', provider: 'gmail', status: 'active', daily_cap: 50 }]),
      { status: 200, headers: jsonHeaders },
    )
  startGoogleResponder = () => new Response('{}', { status: 200, headers: jsonHeaders })
  startMicrosoftResponder = () => new Response('{}', { status: 200, headers: jsonHeaders })
  authMeResponder = () =>
    new Response(JSON.stringify({ user_id: 'u-1', email: 'me@company.com', email_verified: true }), {
      status: 200,
      headers: jsonHeaders,
    })

  // No warmup participants by default, so the pool-idle notice stays off and the
  // OAuth/list tests below are unaffected by it.
  overviewResponder = () => jsonResponse({ pool_size: 0, active: false, mailboxes: [] })
  warmupWriteResponder = () => jsonResponse({ mailbox_id: 'm-1', enabled: true })
  connectResponder = () => jsonResponse({ id: 'm-2', email: 'new@company.com', provider: 'smtp', status: 'active' })
  requests = []

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = input instanceof Request ? input.method.toUpperCase() : 'GET'
      requests.push({ method, url })
      // Email-verification state for the connect gate. Verified by default so
      // the OAuth/connect-flow tests below stay about the flow.
      if (url.includes('/auth/me')) return authMeResponder()
      if (url.endsWith('/warmup/overview')) return overviewResponder()
      // PUT/DELETE /mailboxes/{id}/warmup — the row's inline toggle.
      if (url.endsWith('/warmup')) return warmupWriteResponder()
      if (url.endsWith('/mailboxes') && method === 'POST') return connectResponder()
      if (url.includes('/mailboxes/oauth/google/start')) return startGoogleResponder()
      if (url.includes('/mailboxes/oauth/microsoft/start')) return startMicrosoftResponder()
      // The page also mounts DomainAuthPanel; no domains keeps it off-screen so
      // these tests stay about the mailbox list. domain-auth-panel.test.tsx
      // covers the panel itself.
      if (url.includes('/sending-domains')) return new Response('[]', { status: 200, headers: jsonHeaders })
      return listResponder()
    }),
  )

  // jsdom's window.location.assign is non-configurable and throws on real
  // navigation, so swap in a stub location for the redirect assertion.
  assignMock = vi.fn()
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...ORIGINAL_LOCATION, assign: assignMock, replace: vi.fn(), href: ORIGINAL_LOCATION.href },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  Object.defineProperty(window, 'location', { configurable: true, value: ORIGINAL_LOCATION })
})

/** Opens the topbar Connect menu and selects a provider menu item by name. */
async function selectProvider(name: RegExp) {
  const trigger = await screen.findByRole('button', { name: /^connect mailbox$/i })
  // Menus open on keydown (Enter), not a bare click, in Radix.
  fireEvent.keyDown(trigger, { key: 'Enter' })
  const item = await screen.findByRole('menuitem', { name })
  fireEvent.click(item)
}

const selectGmail = () => selectProvider(/^gmail$/i)
const selectMicrosoft = () => selectProvider(/microsoft 365/i)

test('a 501 Gmail start error closes the menu and surfaces the disabled banner', async () => {
  startGoogleResponder = () =>
    new Response(JSON.stringify({ error: 'gmail oauth not configured' }), { status: 501, headers: jsonHeaders })

  renderWithProviders(<MailboxesPage />)
  await selectGmail()

  // The occluded-banner regression guard: the alert must appear AND the menu
  // must have closed so it isn't hidden underneath the open dropdown.
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/Gmail connect isn't configured on this server\./i)
  await waitFor(() => expect(screen.queryByRole('menuitem', { name: /^gmail$/i })).not.toBeInTheDocument())
  expect(assignMock).not.toHaveBeenCalled()
})

test('a successful Gmail start redirects to the auth_url and leaves the menu open', async () => {
  startGoogleResponder = () =>
    new Response(JSON.stringify({ auth_url: AUTH_URL }), { status: 200, headers: jsonHeaders })

  renderWithProviders(<MailboxesPage />)
  await selectGmail()

  await waitFor(() => expect(assignMock).toHaveBeenCalledWith(AUTH_URL))
  // No error banner, and the menu stays open through the redirect.
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: /^gmail$/i })).toBeInTheDocument()
})

test('a 501 Microsoft 365 start error closes the menu and surfaces the disabled banner', async () => {
  startMicrosoftResponder = () =>
    new Response(JSON.stringify({ error: 'microsoft oauth not configured' }), { status: 501, headers: jsonHeaders })

  renderWithProviders(<MailboxesPage />)
  await selectMicrosoft()

  // Provider-correct disabled copy from the shared mapping, and the menu must
  // close so the alert underneath it is visible.
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/Microsoft 365 connect isn't configured on this server\./i)
  await waitFor(() => expect(screen.queryByRole('menuitem', { name: /microsoft 365/i })).not.toBeInTheDocument())
  expect(assignMock).not.toHaveBeenCalled()
})

test('a non-501 Microsoft start error surfaces the generic transient banner', async () => {
  startMicrosoftResponder = () =>
    new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<MailboxesPage />)
  await selectMicrosoft()

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/Couldn't start Microsoft sign-in — try again\./i)
  await waitFor(() => expect(screen.queryByRole('menuitem', { name: /microsoft 365/i })).not.toBeInTheDocument())
  expect(assignMock).not.toHaveBeenCalled()
})

test('a successful Microsoft 365 start redirects to the auth_url and leaves the menu open', async () => {
  startMicrosoftResponder = () =>
    new Response(JSON.stringify({ auth_url: MS_AUTH_URL }), { status: 200, headers: jsonHeaders })

  renderWithProviders(<MailboxesPage />)
  await selectMicrosoft()

  await waitFor(() => expect(assignMock).toHaveBeenCalledWith(MS_AUTH_URL))
  // No error banner, and the menu stays open through the redirect.
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: /microsoft 365/i })).toBeInTheDocument()
})

// Warmup lives on the mailbox row: its state, and a toggle that doesn't send the
// operator to /app/warmup. The honesty case matters most — a one-mailbox pool has
// no partner to exchange with, so an "enabled" mailbox does nothing, and the page
// has to say that rather than let the user conclude it's broken.

/** Opens one row's overflow menu and returns its warmup toggle item. */
async function openRowWarmupItem(email: string) {
  // Radix menus open on keydown, not a bare click (same as the Connect menu).
  fireEvent.keyDown(await screen.findByRole('button', { name: `Actions for ${email}` }), { key: 'Enter' })
  return screen.findByRole('menuitem', { name: /warming up$/i })
}

test('a single warming mailbox says warming waits for a second mailbox', async () => {
  overviewResponder = () =>
    jsonResponse({ pool_size: 1, active: false, mailboxes: [warmupEntry('m-1', 'sender@gmail.com')] })

  renderWithProviders(<MailboxesPage />)

  expect(
    await screen.findByText(/warming starts once a second mailbox is connected and warming too/i),
  ).toBeInTheDocument()
  // The row agrees, instead of showing a ramp counter frozen at 0/13.
  expect(await screen.findByText(/idle — needs 2/i)).toBeInTheDocument()
})

test('a pool of two says nothing — warmup is actually exchanging mail', async () => {
  listResponder = () =>
    jsonResponse([
      { id: 'm-1', email: 'sender@gmail.com', provider: 'gmail', status: 'active', daily_cap: 50 },
      { id: 'm-2', email: 'second@gmail.com', provider: 'gmail', status: 'active', daily_cap: 50 },
    ])
  overviewResponder = () =>
    jsonResponse({
      pool_size: 2,
      active: true,
      mailboxes: [warmupEntry('m-1', 'sender@gmail.com'), warmupEntry('m-2', 'second@gmail.com')],
    })

  renderWithProviders(<MailboxesPage />)

  await screen.findByText('second@gmail.com')
  expect(screen.queryByText(/warming starts once a second mailbox/i)).not.toBeInTheDocument()
  expect(screen.queryByText(/idle — needs 2/i)).not.toBeInTheDocument()
})

test('a mailbox that is not warming can be started from its own row', async () => {
  renderWithProviders(<MailboxesPage />)

  fireEvent.click(await openRowWarmupItem('sender@gmail.com'))

  await waitFor(() =>
    expect(requests.some((r) => r.method === 'PUT' && r.url.endsWith('/mailboxes/m-1/warmup'))).toBe(true),
  )
})

test('a warming mailbox can be stopped from its own row', async () => {
  overviewResponder = () =>
    jsonResponse({ pool_size: 2, active: true, mailboxes: [warmupEntry('m-1', 'sender@gmail.com')] })
  renderWithProviders(<MailboxesPage />)

  const item = await openRowWarmupItem('sender@gmail.com')
  expect(item).toHaveTextContent(/stop warming up/i)
  fireEvent.click(item)

  await waitFor(() =>
    expect(requests.some((r) => r.method === 'DELETE' && r.url.endsWith('/mailboxes/m-1/warmup'))).toBe(true),
  )
})

test('a failed row toggle reports itself instead of silently doing nothing', async () => {
  warmupWriteResponder = () => jsonResponse({ error: 'warmup unavailable' }, 500)
  renderWithProviders(<MailboxesPage />)

  fireEvent.click(await openRowWarmupItem('sender@gmail.com'))

  expect(await screen.findByRole('alert')).toHaveTextContent(/Couldn't start warmup\. Please try again\./)
})

test('a connect whose warmup enable fails still reads as connected', async () => {
  warmupWriteResponder = () => jsonResponse({ error: 'warmup unavailable' }, 500)
  renderWithProviders(<MailboxesPage />)

  // Open the SMTP/IMAP form through the same menu a user would.
  await selectProvider(/smtp \/ imap/i)
  fireEvent.change(await screen.findByLabelText('Email'), { target: { value: 'new@company.com' } })
  fireEvent.change(screen.getByLabelText('SMTP host'), { target: { value: 'smtp.company.com' } })
  fireEvent.change(screen.getByLabelText('IMAP host'), { target: { value: 'imap.company.com' } })
  fireEvent.change(screen.getByLabelText(/Password/), { target: { value: 'app-password-123' } })
  // Submit the form itself: "Connect mailbox" names both the menu trigger and
  // this submit, and the button path is covered in connect-mailbox-form.test.tsx.
  const form = screen.getByLabelText('SMTP host').closest('form')
  if (!form) throw new Error('the connect form did not render')
  fireEvent.submit(form)

  // The mailbox IS connected — the notice names only the part that failed, and
  // the form closes rather than presenting the whole flow as a failure.
  expect(
    await screen.findByText(/Mailbox connected, but warmup couldn’t be enabled/i),
  ).toBeInTheDocument()
  await waitFor(() => expect(screen.queryByLabelText('SMTP host')).not.toBeInTheDocument())
})

// Email verification: POST /mailboxes and both OAuth-start routes are behind
// `auth.RequireVerified`, so the single entry point into all three carries the
// gate — and it has to explain itself without a hover.
test('an unverified account replaces the Connect menu with a gated, self-explaining button', async () => {
  authMeResponder = () =>
    new Response(JSON.stringify({ user_id: 'u-1', email: 'me@company.com', email_verified: false }), {
      status: 200,
      headers: jsonHeaders,
    })

  renderWithProviders(<MailboxesPage />, { preloadedState: AUTHED })

  await waitFor(() =>
    expect(screen.getByRole('button', { name: /^connect mailbox$/i })).toHaveAttribute(
      'aria-disabled',
      'true',
    ),
  )
  const trigger = screen.getByRole('button', { name: /^connect mailbox$/i })
  const hintId = trigger.getAttribute('aria-describedby')
  expect(document.getElementById(hintId ?? '')).toHaveTextContent(
    /Verify your email address to connect a mailbox\./,
  )

  // No menu to open, so no OAuth start can be attempted from here.
  fireEvent.keyDown(trigger, { key: 'Enter' })
  expect(screen.queryByRole('menuitem', { name: /^gmail$/i })).not.toBeInTheDocument()
})

test('a verified account gets the full Connect menu', async () => {
  renderWithProviders(<MailboxesPage />, { preloadedState: AUTHED })

  await waitFor(() =>
    expect(screen.getByRole('button', { name: /^connect mailbox$/i })).not.toHaveAttribute('aria-disabled'),
  )
  // Opened, not selected: this test is about the gate being off, and selecting
  // a provider would kick off a start request the OAuth tests above own.
  fireEvent.keyDown(screen.getByRole('button', { name: /^connect mailbox$/i }), { key: 'Enter' })
  expect(await screen.findByRole('menuitem', { name: /^gmail$/i })).toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: /microsoft 365/i })).toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: /smtp \/ imap/i })).toBeInTheDocument()
})

test('an m365 mailbox row shows the Microsoft 365 provider tag and "· API" line', async () => {
  listResponder = () =>
    new Response(
      JSON.stringify([{ id: 'm-2', email: 'sender@contoso.com', provider: 'm365', status: 'active', daily_cap: 50 }]),
      { status: 200, headers: jsonHeaders },
    )

  renderWithProviders(<MailboxesPage />)

  expect(await screen.findByText('sender@contoso.com')).toBeInTheDocument()
  expect(screen.getByText('Microsoft 365')).toBeInTheDocument()
  expect(screen.getByText('Microsoft 365 · API')).toBeInTheDocument()
})

test('the empty state trigger has a distinct accessible name from the topbar trigger', async () => {
  listResponder = () => new Response(JSON.stringify([]), { status: 200, headers: jsonHeaders })

  renderWithProviders(<MailboxesPage />)

  // The empty block renders once the (empty) list query resolves; both triggers
  // show the same visible label, but screen readers get distinct names.
  expect(await screen.findByRole('button', { name: /connect your first mailbox/i })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /^connect mailbox$/i })).toBeInTheDocument()
})

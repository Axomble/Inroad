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
let assignMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  listResponder = () =>
    new Response(
      JSON.stringify([{ id: 'm-1', email: 'sender@gmail.com', provider: 'gmail', status: 'active', daily_cap: 50 }]),
      { status: 200, headers: jsonHeaders },
    )
  startGoogleResponder = () => new Response('{}', { status: 200, headers: jsonHeaders })
  startMicrosoftResponder = () => new Response('{}', { status: 200, headers: jsonHeaders })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/mailboxes/oauth/google/start')) return startGoogleResponder()
      if (url.includes('/mailboxes/oauth/microsoft/start')) return startMicrosoftResponder()
      // The list is grouped by sending domain; returning no verdicts keeps those
      // headings bare so these tests stay about the mailbox rows.
      // domain-auth-header.test.tsx covers the heading itself.
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

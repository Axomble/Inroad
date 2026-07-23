import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { MailboxesPage } from './mailboxes-page'

// MailboxesPage embeds OauthCallbackBanner, which reads the route search via
// getRouteApi and navigates via useNavigate — stub both (empty search => the
// callback banner renders nothing), same as the other feature-page tests.
vi.mock('@tanstack/react-router', () => ({
  getRouteApi: () => ({ useSearch: () => ({}) }),
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
const ORIGINAL_LOCATION = window.location

// Per-test responders for the two endpoints MailboxesPage hits.
let listResponder: () => Response
let startResponder: () => Response
let assignMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  listResponder = () =>
    new Response(
      JSON.stringify([{ id: 'm-1', email: 'sender@gmail.com', provider: 'gmail', status: 'active', daily_cap: 50 }]),
      { status: 200, headers: jsonHeaders },
    )
  startResponder = () => new Response('{}', { status: 200, headers: jsonHeaders })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/mailboxes/oauth/google/start')) return startResponder()
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

/** Opens the topbar Connect menu and selects the Gmail item. */
async function selectGmail() {
  const trigger = await screen.findByRole('button', { name: /^connect mailbox$/i })
  // Menus open on keydown (Enter), not a bare click, in Radix.
  fireEvent.keyDown(trigger, { key: 'Enter' })
  const gmail = await screen.findByRole('menuitem', { name: /gmail/i })
  fireEvent.click(gmail)
}

test('a 501 start error closes the menu and surfaces the disabled banner', async () => {
  startResponder = () =>
    new Response(JSON.stringify({ error: 'gmail oauth not configured' }), { status: 501, headers: jsonHeaders })

  renderWithProviders(<MailboxesPage />)
  await selectGmail()

  // The occluded-banner regression guard: the alert must appear AND the menu
  // must have closed so it isn't hidden underneath the open dropdown.
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/Gmail connect isn't configured on this server\./i)
  await waitFor(() => expect(screen.queryByRole('menuitem', { name: /gmail/i })).not.toBeInTheDocument())
  expect(assignMock).not.toHaveBeenCalled()
})

test('a successful start redirects to the auth_url and leaves the menu open', async () => {
  startResponder = () => new Response(JSON.stringify({ auth_url: AUTH_URL }), { status: 200, headers: jsonHeaders })

  renderWithProviders(<MailboxesPage />)
  await selectGmail()

  await waitFor(() => expect(assignMock).toHaveBeenCalledWith(AUTH_URL))
  // No error banner, and the menu stays open through the redirect.
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: /gmail/i })).toBeInTheDocument()
})

test('the empty state trigger has a distinct accessible name from the topbar trigger', async () => {
  listResponder = () => new Response(JSON.stringify([]), { status: 200, headers: jsonHeaders })

  renderWithProviders(<MailboxesPage />)

  // The empty block renders once the (empty) list query resolves; both triggers
  // show the same visible label, but screen readers get distinct names.
  expect(await screen.findByRole('button', { name: /connect your first mailbox/i })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /^connect mailbox$/i })).toBeInTheDocument()
})

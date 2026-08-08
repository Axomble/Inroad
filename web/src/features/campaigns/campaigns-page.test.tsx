import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { CampaignsPage } from './campaigns-page'

// CampaignRow's launch flow is the send trigger: a broken status→message branch
// maps directly to double-sends (409 not surfaced) or silent no-ops (422/500
// swallowed). These tests lock the exact copy the refactored component renders
// via `httpStatus`, plus the success path (mutation fired, no error shown).

// The page's filter/sort live in the URL (`useUrlState`) and a row click
// navigates to the campaign's own route, so stub the router: empty search means
// no filter and the default sort. `navigate` is hoisted to a stable spy (not a
// fresh no-op per render) so a test can assert it was — or, for the
// LifecycleMenu regression below, was NOT — called.
const navigateMock = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useSearch: () => ({}),
  useNavigate: () => navigateMock,
}))

// Radix DropdownMenu/AlertDialog (LifecycleMenu, in the row's overflow menu)
// drive open/close through pointer events jsdom doesn't fully implement;
// polyfill what they touch (same shim mailboxes-page.test.tsx uses).
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

type CapturedRequest = { method: string; url: string }

let campaigns: Array<{ id: string; name: string; subject: string; status: string }>
let launchResponder: () => Response
let preflightResponder: () => Response
let authMeResponder: () => Response
let requests: CapturedRequest[]

/** Signed in, so a row's `useEmailVerified` actually queries /auth/me. */
const AUTHED = { auth: { status: 'authed' as const, accessToken: 'token' } }

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

beforeEach(() => {
  requests = []
  navigateMock.mockClear()
  campaigns = [{ id: 'c-1', name: 'Q3 Outbound', subject: 'Quick question', status: 'draft' }]
  // A successful launch echoes queue counts; overridden per-test for error paths.
  launchResponder = () => jsonResponse({ queued: 3, total_enrolled: 3, failed_enqueue_count: 0 })
  // The preflight gate the Launch button now opens through — all-pass by
  // default so these tests exercise the same launch-mutation branches they
  // did before the dialog existed.
  authMeResponder = () => jsonResponse({ user_id: 'u-1', email: 'me@company.com', email_verified: true })
  preflightResponder = () =>
    jsonResponse({
      ready: true,
      checks: [{ id: 'sequence_steps', severity: 'pass', title: 'Sequence has steps', detail: '1 step.', remedy: '' }],
    })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // RTK Query passes a `Request` for the launch/delete mutations and
      // `(url, init)` for the plain list GET — read method/url from whichever
      // the caller used.
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()
      requests.push({ method, url })

      if (url.includes('/auth/me')) return authMeResponder()
      if (url.endsWith('/preflight')) return preflightResponder()
      if (url.endsWith('/launch') && method === 'POST') return launchResponder()
      if (method === 'DELETE') return new Response(null, { status: 204 })
      return jsonResponse(campaigns)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Opens the row's Launch button, waits for the preflight dialog's checks to
 * load, then confirms — the row's `Launch` control now gates through that
 * dialog rather than firing the launch mutation directly. */
async function clickLaunch() {
  const launch = await screen.findByRole('button', { name: /^launch$/i })
  fireEvent.click(launch)

  const dialog = await screen.findByRole('alertdialog')
  const confirm = await within(dialog).findByRole('button', { name: /^launch campaign$/i })
  await waitFor(() => expect(confirm).toBeEnabled())
  fireEvent.click(confirm)
}

// Email verification: POST /campaigns/{id}/launch is behind
// `auth.RequireVerified`, so the row's Launch control is gated — and because
// the gate is only an affordance, the 403 keeps its own copy too.
test('an unverified account cannot start a launch, and the Launch control says why', async () => {
  authMeResponder = () => jsonResponse({ user_id: 'u-1', email: 'me@company.com', email_verified: false })

  renderWithProviders(<CampaignsPage />, { preloadedState: AUTHED })

  await waitFor(() =>
    expect(screen.getByRole('button', { name: /^launch$/i })).toHaveAttribute('aria-disabled', 'true'),
  )
  const launch = screen.getByRole('button', { name: /^launch$/i })
  const hintId = launch.getAttribute('aria-describedby')
  expect(document.getElementById(hintId ?? '')).toHaveTextContent(
    /Verify your email address to launch a campaign\./,
  )

  fireEvent.click(launch)
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(requests.some((r) => r.url.endsWith('/preflight'))).toBe(false)
  expect(requests.some((r) => r.method === 'POST' && r.url.endsWith('/launch'))).toBe(false)
})

test('a 403 email_not_verified launch surfaces the verification copy, not "Launch failed."', async () => {
  launchResponder = () => jsonResponse({ error: 'email_not_verified' }, 403)

  renderWithProviders(<CampaignsPage />)
  await clickLaunch()

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/Verify your email address to launch a campaign\./)
  expect(alert).not.toHaveTextContent(/Launch failed\./)
})

test('clicking Launch opens the preflight dialog instead of firing the launch mutation immediately', async () => {
  renderWithProviders(<CampaignsPage />)

  const launch = await screen.findByRole('button', { name: /^launch$/i })
  fireEvent.click(launch)

  expect(await screen.findByRole('alertdialog', { name: /launch.*q3 outbound/i })).toBeInTheDocument()
  expect(requests.some((r) => r.method === 'POST' && r.url.endsWith('/launch'))).toBe(false)
  expect(requests.some((r) => r.url.endsWith('/preflight'))).toBe(true)
})

test('a fail check in the preflight report blocks the row Launch confirm, and Cancel closes without launching', async () => {
  preflightResponder = () =>
    jsonResponse({
      ready: false,
      checks: [
        {
          id: 'sender_pool',
          severity: 'fail',
          title: 'No eligible sender',
          detail: 'No enabled sender has an active mailbox.',
          remedy: 'Add a connected mailbox to the sender pool.',
        },
      ],
    })

  renderWithProviders(<CampaignsPage />)
  fireEvent.click(await screen.findByRole('button', { name: /^launch$/i }))

  const dialog = await screen.findByRole('alertdialog')
  const confirm = await within(dialog).findByRole('button', { name: /^launch campaign$/i })
  await waitFor(() => expect(confirm).toBeDisabled())

  fireEvent.click(within(dialog).getByRole('button', { name: /cancel/i }))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  expect(requests.some((r) => r.method === 'POST' && r.url.endsWith('/launch'))).toBe(false)
})

test('a 409 launch surfaces "Already launched." (guards against double-sends)', async () => {
  launchResponder = () => jsonResponse({ error: 'already launched' }, 409)

  renderWithProviders(<CampaignsPage />)
  await clickLaunch()

  expect(await screen.findByText(/Already launched\./)).toBeInTheDocument()
  expect(requests.some((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/launch'))).toBe(true)
})

test('a 422 launch surfaces "Target list is empty."', async () => {
  launchResponder = () => jsonResponse({ error: 'target list is empty' }, 422)

  renderWithProviders(<CampaignsPage />)
  await clickLaunch()

  expect(await screen.findByText(/Target list is empty\./)).toBeInTheDocument()
})

test('any other launch failure surfaces the generic "Launch failed." fallback', async () => {
  launchResponder = () => jsonResponse({ error: 'boom' }, 500)

  renderWithProviders(<CampaignsPage />)
  await clickLaunch()

  expect(await screen.findByText(/Launch failed\./)).toBeInTheDocument()
  // The specific-branch copy must NOT appear for an unmapped status.
  expect(screen.queryByText(/Already launched\./)).not.toBeInTheDocument()
  expect(screen.queryByText(/Target list is empty\./)).not.toBeInTheDocument()
})

test('a successful launch fires the mutation and shows no error', async () => {
  renderWithProviders(<CampaignsPage />)
  await clickLaunch()

  // The POST lands with the campaign id and no error copy is rendered.
  await waitFor(() =>
    expect(requests.some((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/launch'))).toBe(true),
  )
  await waitFor(() => {
    expect(screen.queryByText(/Launch failed\./)).not.toBeInTheDocument()
    expect(screen.queryByText(/Already launched\./)).not.toBeInTheDocument()
    expect(screen.queryByText(/Target list is empty\./)).not.toBeInTheDocument()
  })
})

// Regression guard: the row itself navigates on click, and LifecycleMenu's
// dropdown/AlertDialog content is portalled elsewhere in the DOM but still
// bubbles click events through the *React* tree back up to that row — without
// LifecycleMenu stopping that propagation, confirming a delete from inside the
// portalled dialog would immediately navigate to the campaign it just deleted.
test('confirming delete from the row overflow menu fires the DELETE request without navigating the row', async () => {
  renderWithProviders(<CampaignsPage />)

  const trigger = await screen.findByRole('button', { name: /actions for q3 outbound/i })
  // Radix's DropdownMenu opens on keydown (Enter), not a bare click.
  fireEvent.keyDown(trigger, { key: 'Enter' })
  fireEvent.click(await screen.findByRole('menuitem', { name: /delete campaign/i }))

  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: /delete campaign/i }))

  await waitFor(() =>
    expect(requests.some((r) => r.method === 'DELETE' && r.url.endsWith('/campaigns/c-1'))).toBe(true),
  )
  expect(navigateMock).not.toHaveBeenCalled()
})

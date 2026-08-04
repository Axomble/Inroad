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
let requests: CapturedRequest[]

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

beforeEach(() => {
  requests = []
  navigateMock.mockClear()
  campaigns = [{ id: 'c-1', name: 'Q3 Outbound', subject: 'Quick question', status: 'draft' }]
  // A successful launch echoes queue counts; overridden per-test for error paths.
  launchResponder = () => jsonResponse({ queued: 3, total_enrolled: 3, failed_enqueue_count: 0 })

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

async function clickLaunch() {
  const launch = await screen.findByRole('button', { name: /^launch$/i })
  fireEvent.click(launch)
}

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

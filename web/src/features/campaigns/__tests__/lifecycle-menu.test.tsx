import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { Campaign } from '@/store/api'
import { LifecycleMenu } from '../lifecycle-menu'

// Radix DropdownMenu/AlertDialog drive open/close through pointer + keyboard
// events jsdom doesn't fully implement; polyfill what they touch so the menu
// and confirm dialogs can actually open under test (same shim
// mailboxes-page.test.tsx and active-sessions.test.tsx use).
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

function campaign(overrides: Partial<Campaign> = {}): Campaign {
  return { id: 'c-1', name: 'Q3 Outbound', subject: 'Quick question', status: 'draft', ...overrides }
}

type CapturedRequest = { method: string; url: string }
let requests: CapturedRequest[]
let pauseResponder: () => Response

beforeEach(() => {
  requests = []
  pauseResponder = () => new Response(null, { status: 204 })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // RTK Query passes a `Request` for POST/PUT/DELETE mutations.
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()
      requests.push({ method, url })

      if (url.endsWith('/pause') && method === 'POST') return pauseResponder()
      if (method === 'DELETE') return new Response(null, { status: 204 })
      if (method === 'POST' && url.endsWith('/resume')) return new Response(null, { status: 204 })
      return new Response(null, { status: 204 })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Opens the row's overflow menu. Radix opens on keydown (Enter), not a bare click. */
async function openMenu() {
  const trigger = await screen.findByRole('button', { name: /actions for q3 outbound/i })
  fireEvent.keyDown(trigger, { key: 'Enter' })
}

test('a running campaign shows Pause, not Resume or Delete', async () => {
  renderWithProviders(<LifecycleMenu campaign={campaign({ status: 'running' })} />)
  await openMenu()

  expect(await screen.findByRole('menuitem', { name: /pause campaign/i })).toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /resume campaign/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /delete campaign/i })).not.toBeInTheDocument()
})

test('a paused campaign shows Resume, not Pause', async () => {
  renderWithProviders(<LifecycleMenu campaign={campaign({ status: 'paused' })} />)
  await openMenu()

  expect(await screen.findByRole('menuitem', { name: /resume campaign/i })).toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /pause campaign/i })).not.toBeInTheDocument()
})

test('a draft campaign shows Delete', async () => {
  renderWithProviders(<LifecycleMenu campaign={campaign({ status: 'draft' })} />)
  await openMenu()

  expect(await screen.findByRole('menuitem', { name: /delete campaign/i })).toBeInTheDocument()
})

test('every status offers Rename…, and a finished campaign offers no lifecycle transition', async () => {
  renderWithProviders(<LifecycleMenu campaign={campaign({ status: 'done' })} />)
  await openMenu()

  expect(await screen.findByRole('menuitem', { name: /rename/i })).toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /pause campaign/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /resume campaign/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /delete campaign/i })).not.toBeInTheDocument()
})

test('resuming a paused campaign fires the mutation with no confirmation dialog', async () => {
  renderWithProviders(<LifecycleMenu campaign={campaign({ status: 'paused' })} />)
  await openMenu()

  fireEvent.click(await screen.findByRole('menuitem', { name: /resume campaign/i }))

  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  await waitFor(() =>
    expect(requests.some((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/resume'))).toBe(true),
  )
})

test('deleting a draft campaign requires dialog confirmation, naming the campaign, before the DELETE request fires', async () => {
  renderWithProviders(<LifecycleMenu campaign={campaign({ status: 'draft' })} />)
  await openMenu()
  fireEvent.click(await screen.findByRole('menuitem', { name: /delete campaign/i }))

  const dialog = await screen.findByRole('alertdialog')
  expect(within(dialog).getByText(/q3 outbound/i)).toBeInTheDocument()
  expect(requests.some((r) => r.method === 'DELETE')).toBe(false)

  fireEvent.click(within(dialog).getByRole('button', { name: /delete campaign/i }))

  await waitFor(() =>
    expect(requests.some((r) => r.method === 'DELETE' && r.url.endsWith('/campaigns/c-1'))).toBe(true),
  )
})

test('a 409 on pause renders the API error copy returned by the server', async () => {
  pauseResponder = () => jsonResponse({ error: 'campaign is not running' }, 409)

  renderWithProviders(<LifecycleMenu campaign={campaign({ status: 'running' })} />)
  await openMenu()
  fireEvent.click(await screen.findByRole('menuitem', { name: /pause campaign/i }))

  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: /^pause campaign$/i }))

  expect(await screen.findByText('campaign is not running')).toBeInTheDocument()
  // The confirm dialog closes once the (failed) request settles.
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
})

import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { CampaignsPage } from './campaigns-page'

// CampaignRow's launch flow is the send trigger: a broken status→message branch
// maps directly to double-sends (409 not surfaced) or silent no-ops (422/500
// swallowed). These tests lock the exact copy the refactored component renders
// via `httpStatus`, plus the success path (mutation fired, no error shown).

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
  campaigns = [{ id: 'c-1', name: 'Q3 Outbound', subject: 'Quick question', status: 'draft' }]
  // A successful launch echoes queue counts; overridden per-test for error paths.
  launchResponder = () => jsonResponse({ queued: 3, total_enrolled: 3, failed_enqueue_count: 0 })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // RTK Query passes a `Request` for the launch mutation and `(url, init)`
      // for the plain list GET — read method/url from whichever the caller used.
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()
      requests.push({ method, url })

      if (url.endsWith('/launch') && method === 'POST') return launchResponder()
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

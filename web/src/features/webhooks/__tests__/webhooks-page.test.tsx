import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { WebhookEndpoint } from '@/store/api'
import { WebhooksPage } from '../webhooks-page'

// Radix AlertDialog touches pointer + scroll APIs jsdom doesn't implement.
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

function endpoint(overrides: Partial<WebhookEndpoint> = {}): WebhookEndpoint {
  return {
    id: 'ep-1',
    url: 'https://example.com/hooks/inroad',
    description: 'Zapier — reply routing',
    event_types: ['reply.received'],
    active: true,
    created_at: new Date(Date.now() - 3_600_000).toISOString(),
    updated_at: new Date(Date.now() - 3_600_000).toISOString(),
    ...overrides,
  }
}

let listItems: WebhookEndpoint[]
let createResponder: () => Response

beforeEach(() => {
  listItems = []
  createResponder = () =>
    new Response(JSON.stringify({ ...endpoint(), secret: 'c2VjcmV0LXZhbHVlLTEyMzQ1Ng==' }), {
      status: 201,
      headers: jsonHeaders,
    })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? request?.method ?? 'GET'

      if (/\/webhook-endpoints$/.test(url.split('?')[0] ?? '')) {
        if (method === 'POST') {
          const response = createResponder()
          // Mirrors a real server: a successful create is a row the next list
          // read will include, so the invalidation this feature's api.ts wires
          // up has something to actually refetch.
          if (response.status < 300) {
            // Structurally a WebhookEndpoint plus `secret`; the list response
            // this feature reads never includes that field, but the extra
            // property does not affect what the row under test renders.
            const created = (await response.clone().json()) as WebhookEndpoint & { secret: string }
            listItems = [...listItems, created]
          }
          return response
        }
        return new Response(JSON.stringify({ items: listItems }), { status: 200, headers: jsonHeaders })
      }
      return new Response('not found', { status: 404 })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/* -------------------------------------------------------------- the list */

test('endpoints are listed with their url, events and active state', async () => {
  listItems = [endpoint()]
  renderWithProviders(<WebhooksPage />)

  expect(await screen.findByText('https://example.com/hooks/inroad')).toBeInTheDocument()
  expect(screen.getByText('Reply received')).toBeInTheDocument()
  expect(screen.getByText('Active')).toBeInTheDocument()
})

test('an empty workspace explains what a webhook endpoint is for', async () => {
  renderWithProviders(<WebhooksPage />)

  expect(await screen.findByText('No webhook endpoints yet')).toBeInTheDocument()
  expect(screen.getByText(/register a receiver url/i)).toBeInTheDocument()
})

test('a failed list read is not shown as an empty, healthy list', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify({ message: 'nope' }), { status: 500, headers: jsonHeaders })),
  )

  renderWithProviders(<WebhooksPage />)

  expect(await screen.findByText("Couldn't load webhook endpoints")).toBeInTheDocument()
  expect(screen.queryByText('No webhook endpoints yet')).not.toBeInTheDocument()
})

/* ------------------------------------------------------------- creating */

test('creating an endpoint surfaces the one-time secret and says it cannot be shown again', async () => {
  renderWithProviders(<WebhooksPage />)

  await screen.findByText('No webhook endpoints yet')
  fireEvent.click(screen.getByRole('button', { name: /new endpoint/i }))

  fireEvent.change(screen.getByLabelText('Receiver URL'), {
    target: { value: 'https://example.com/hooks/inroad' },
  })
  fireEvent.click(screen.getByRole('button', { name: /^create endpoint$/i }))

  expect(await screen.findByText('c2VjcmV0LXZhbHVlLTEyMzQ1Ng==')).toBeInTheDocument()
  expect(screen.getByText(/only time inroad shows/i)).toBeInTheDocument()
  expect(screen.getByText(/cannot be fetched again/i)).toBeInTheDocument()

  // Escape must not close the reveal — only the explicit acknowledgement may.
  fireEvent.keyDown(screen.getByRole('alertdialog'), { key: 'Escape' })
  expect(screen.getByText('c2VjcmV0LXZhbHVlLTEyMzQ1Ng==')).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /i.ve copied it/i }))
  await waitFor(() => expect(screen.queryByText('c2VjcmV0LXZhbHVlLTEyMzQ1Ng==')).not.toBeInTheDocument())

  // The list refetched and now shows the endpoint — metadata only, no secret.
  expect(await screen.findByText('https://example.com/hooks/inroad')).toBeInTheDocument()
})

test('leaving every event box unchecked is a valid subscription (subscribe to all)', async () => {
  // A holder object, not a bare `let` — a `let` reassigned only inside the
  // fetch mock's closure narrows to its initial `null` at the `expect` below.
  const captured: { body: { event_types?: string[] } | null } = { body: null }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? request?.method ?? 'GET'
      if (/\/webhook-endpoints$/.test(url.split('?')[0] ?? '') && method === 'POST') {
        const raw = request ? await request.text() : String(init?.body ?? '{}')
        captured.body = JSON.parse(raw) as { event_types?: string[] }
        return createResponder()
      }
      return new Response(JSON.stringify({ items: [] }), { status: 200, headers: jsonHeaders })
    }),
  )

  renderWithProviders(<WebhooksPage />)
  await screen.findByText('No webhook endpoints yet')
  fireEvent.click(screen.getByRole('button', { name: /new endpoint/i }))
  fireEvent.change(screen.getByLabelText('Receiver URL'), { target: { value: 'https://example.com/hook' } })
  fireEvent.click(screen.getByRole('button', { name: /^create endpoint$/i }))

  await screen.findByText(/only time inroad shows/i)
  expect(captured.body?.event_types).toEqual([])
})

test('a failed create shows mapped copy, never a raw status code or a bare failure', async () => {
  createResponder = () =>
    new Response(JSON.stringify({ message: 'blocked' }), { status: 422, headers: jsonHeaders })

  renderWithProviders(<WebhooksPage />)
  await screen.findByText('No webhook endpoints yet')
  fireEvent.click(screen.getByRole('button', { name: /new endpoint/i }))
  fireEvent.change(screen.getByLabelText('Receiver URL'), { target: { value: 'http://169.254.169.254/' } })
  fireEvent.click(screen.getByRole('button', { name: /^create endpoint$/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/ssrf guard/i)
  expect(alert).not.toHaveTextContent('422')
  expect(alert.textContent?.trim().toLowerCase()).not.toBe('something went wrong.')

  // No secret was ever shown for a failed create.
  expect(screen.queryByText(/copy the signing secret now/i)).not.toBeInTheDocument()
})

test('create is disabled until a URL is entered', async () => {
  renderWithProviders(<WebhooksPage />)
  await screen.findByText('No webhook endpoints yet')
  fireEvent.click(screen.getByRole('button', { name: /new endpoint/i }))

  expect(screen.getByRole('button', { name: /^create endpoint$/i })).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Receiver URL'), { target: { value: 'https://example.com/hook' } })
  expect(screen.getByRole('button', { name: /^create endpoint$/i })).toBeEnabled()
})

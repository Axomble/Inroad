import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { WebhookEndpoint } from '@/store/api'
import { WebhookEndpointRow } from '../webhook-endpoint-row'

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
    description: '',
    event_types: [],
    active: true,
    created_at: new Date(Date.now() - 3_600_000).toISOString(),
    updated_at: new Date(Date.now() - 3_600_000).toISOString(),
    ...overrides,
  }
}

let writes: string[]
let deleteResponder: () => Response
let rotateResponder: () => Response
let pingResponder: () => Response

beforeEach(() => {
  writes = []
  deleteResponder = () => new Response(null, { status: 204 })
  rotateResponder = () =>
    new Response(JSON.stringify({ ...endpoint(), secret: 'bmV3LXNlY3JldC12YWx1ZQ==' }), {
      status: 200,
      headers: jsonHeaders,
    })
  pingResponder = () =>
    new Response(
      JSON.stringify({
        id: 'dl-1',
        event_type: 'ping',
        status: 'pending',
        attempts: 0,
        last_error: '',
        response_status: null,
        created_at: new Date().toISOString(),
        delivered_at: null,
      }),
      { status: 202, headers: jsonHeaders },
    )

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? request?.method ?? 'GET'

      if (/\/deliveries/.test(url)) {
        return new Response(JSON.stringify({ items: [], next_cursor: null }), { status: 200, headers: jsonHeaders })
      }
      if (/\/rotate-secret$/.test(url)) {
        writes.push(`${method} ${url}`)
        return rotateResponder()
      }
      if (/\/ping$/.test(url)) {
        writes.push(`${method} ${url}`)
        return pingResponder()
      }
      if (/\/webhook-endpoints\/[^/]+$/.test(url) && method === 'DELETE') {
        writes.push(`${method} ${url}`)
        return deleteResponder()
      }
      return new Response(JSON.stringify({ items: [] }), { status: 200, headers: jsonHeaders })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/* --------------------------------------------------------------- delete */

test('delete asks for confirmation before firing the mutation', async () => {
  renderWithProviders(<WebhookEndpointRow endpoint={endpoint()} />)

  fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
  expect(await screen.findByText(/can't be undone/i)).toBeInTheDocument()

  // Nothing sent yet — the dialog is a gate, not a progress indicator.
  expect(writes).toEqual([])
})

test('cancelling the delete confirmation sends nothing', async () => {
  renderWithProviders(<WebhookEndpointRow endpoint={endpoint()} />)

  fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
  await screen.findByText(/can't be undone/i)
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

  await waitFor(() => expect(screen.queryByText(/can't be undone/i)).not.toBeInTheDocument())
  expect(writes).toEqual([])
})

test('confirming delete fires the mutation exactly once', async () => {
  renderWithProviders(<WebhookEndpointRow endpoint={endpoint()} />)

  fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Delete endpoint' }))

  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toMatch(/DELETE .*\/webhook-endpoints\/ep-1$/)
})

test('a failed delete is reported and the row is not left silently stuck', async () => {
  deleteResponder = () => new Response(JSON.stringify({ message: 'gone' }), { status: 404, headers: jsonHeaders })

  renderWithProviders(<WebhookEndpointRow endpoint={endpoint()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Delete endpoint' }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/no longer here/i)
})

/* --------------------------------------------------------------- rotate */

test('rotating the secret confirms, then reveals the new secret and cannot be dismissed early', async () => {
  renderWithProviders(<WebhookEndpointRow endpoint={endpoint()} />)

  fireEvent.click(screen.getByRole('button', { name: 'Rotate secret' }))
  expect(await screen.findByText(/stops verifying immediately/i)).toBeInTheDocument()
  expect(writes).toEqual([])

  fireEvent.click(screen.getByRole('button', { name: 'Confirm rotation' }))

  expect(await screen.findByText('bmV3LXNlY3JldC12YWx1ZQ==')).toBeInTheDocument()
  expect(screen.getByText(/only time the new signing secret is shown/i)).toBeInTheDocument()
  expect(writes).toEqual(['POST http://localhost:5173/api/v1/webhook-endpoints/ep-1/rotate-secret'])

  // Escape must not close the reveal.
  fireEvent.keyDown(screen.getByRole('alertdialog'), { key: 'Escape' })
  expect(screen.getByText('bmV3LXNlY3JldC12YWx1ZQ==')).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /i.ve copied it/i }))
  await waitFor(() => expect(screen.queryByText('bmV3LXNlY3JldC12YWx1ZQ==')).not.toBeInTheDocument())
})

test('a failed rotate is reported rather than silently doing nothing', async () => {
  rotateResponder = () => new Response(JSON.stringify({ message: 'gone' }), { status: 404, headers: jsonHeaders })

  renderWithProviders(<WebhookEndpointRow endpoint={endpoint()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Rotate secret' }))
  await screen.findByText(/stops verifying immediately/i)
  fireEvent.click(screen.getByRole('button', { name: 'Confirm rotation' }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/no longer here/i)
  expect(screen.queryByText(/copy the signing secret now/i)).not.toBeInTheDocument()
})

/* ----------------------------------------------------------------- ping */

test('sending a test ping surfaces that the delivery was queued', async () => {
  renderWithProviders(<WebhookEndpointRow endpoint={endpoint()} />)

  fireEvent.click(screen.getByRole('button', { name: /send test ping/i }))

  expect(await screen.findByRole('status')).toHaveTextContent(/test delivery was queued/i)
  expect(writes).toEqual(['POST http://localhost:5173/api/v1/webhook-endpoints/ep-1/ping'])
})

test('a failed ping is reported with mapped copy', async () => {
  pingResponder = () => new Response(JSON.stringify({ message: 'gone' }), { status: 404, headers: jsonHeaders })

  renderWithProviders(<WebhookEndpointRow endpoint={endpoint()} />)
  fireEvent.click(screen.getByRole('button', { name: /send test ping/i }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/no longer here/i)
})

/* ------------------------------------------------------------ deliveries */

test('the delivery log is hidden until asked for', async () => {
  renderWithProviders(<WebhookEndpointRow endpoint={endpoint()} />)

  expect(screen.queryByText(/no deliveries yet/i)).not.toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /deliveries/i }))

  expect(await screen.findByText(/no deliveries yet/i)).toBeInTheDocument()
})

import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { LabelPicker } from '../label-picker'
import type { InboxLabel } from '../api'

// Radix DropdownMenu drives open/close from pointer events jsdom doesn't fully
// implement; polyfill what it touches, and open via keyboard below.
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

interface Call {
  method: string
  path: string
  body: string
}

let calls: Call[]
let labels: InboxLabel[]
let createStatus: number

function label(id: string, name: string, color = '#3b82f6'): InboxLabel {
  return { id, name, color, created_at: '2026-08-01T00:00:00Z', updated_at: '2026-08-01T00:00:00Z' }
}

beforeEach(() => {
  calls = []
  createStatus = 200
  labels = [label('l-1', 'Invoices'), label('l-2', 'Partnerships', '#ef4444')]

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      const body = isRequest ? await input.clone().text() : typeof init?.body === 'string' ? init.body : ''
      calls.push({ method, path: url.pathname, body })

      if (url.pathname.endsWith('/inbox/labels') && method === 'GET') {
        return new Response(JSON.stringify({ labels }), { status: 200, headers: jsonHeaders })
      }
      if (url.pathname.endsWith('/inbox/labels') && method === 'POST') {
        if (createStatus !== 200) {
          return new Response(JSON.stringify({ error: 'nope' }), { status: createStatus, headers: jsonHeaders })
        }
        const parsed = JSON.parse(body) as { name: string }
        // Mirrors the server's search-or-create: an existing name resolves to
        // the existing label rather than minting a duplicate.
        const existing = labels.find((l) => l.name.toLowerCase() === parsed.name.toLowerCase())
        const created = existing ?? label('l-new', parsed.name)
        if (!existing) labels = [...labels, created]
        return new Response(JSON.stringify(created), { status: 200, headers: jsonHeaders })
      }
      // Assign / unassign.
      if (url.pathname.includes('/labels/')) return new Response(null, { status: 204 })
      return new Response(JSON.stringify({ error: 'unhandled' }), { status: 404, headers: jsonHeaders })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Radix opens on Enter from the trigger — the keyboard user's route. */
function openPicker() {
  fireEvent.keyDown(screen.getByRole('button', { name: /label this thread/i }), { key: 'Enter' })
}

function labelCalls(): Call[] {
  return calls.filter((c) => c.path.includes('/labels'))
}

test('the picker lists the workspace labels once opened', async () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  openPicker()

  expect(await screen.findByText('Invoices')).toBeInTheDocument()
  expect(screen.getByText('Partnerships')).toBeInTheDocument()
})

// The taxonomy is only fetched on open: the picker sits on every thread, and a
// list request per rendered row would be wasteful.
test('no labels are fetched until the picker is opened', async () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  await waitFor(() => expect(screen.getByRole('button', { name: /label this thread/i })).toBeInTheDocument())
  expect(labelCalls()).toHaveLength(0)

  openPicker()
  await waitFor(() => expect(labelCalls().some((c) => c.method === 'GET')).toBe(true))
})

test('typing filters the list', async () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  openPicker()
  await screen.findByText('Invoices')

  fireEvent.change(screen.getByLabelText(/search or create a label/i), { target: { value: 'part' } })

  await waitFor(() => expect(screen.queryByText('Invoices')).not.toBeInTheDocument())
  expect(screen.getByText('Partnerships')).toBeInTheDocument()
})

test('selecting an unapplied label assigns it', async () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  openPicker()
  fireEvent.click(await screen.findByText('Invoices'))

  await waitFor(() => {
    const assign = labelCalls().find((c) => c.method === 'PUT')
    expect(assign?.path).toBe('/api/v1/inbox/threads/t-1/labels/l-1')
  })
})

test('selecting an applied label removes it — the item is a toggle', async () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={[label('l-1', 'Invoices')]} />)
  openPicker()
  fireEvent.click(await screen.findByText('Invoices'))

  await waitFor(() => {
    const remove = labelCalls().find((c) => c.method === 'DELETE')
    expect(remove?.path).toBe('/api/v1/inbox/threads/t-1/labels/l-1')
  })
})

test('an applied label is marked as such for a screen reader, not by colour alone', async () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={[label('l-1', 'Invoices')]} />)
  openPicker()
  await screen.findByText('Invoices')

  expect(screen.getByText('applied')).toBeInTheDocument()
})

test('a new name offers creation, then creates and applies it', async () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  openPicker()
  await screen.findByText('Invoices')

  fireEvent.change(screen.getByLabelText(/search or create a label/i), { target: { value: 'Renewals' } })
  fireEvent.click(await screen.findByText(/Create/))

  await waitFor(() => {
    const created = labelCalls().find((c) => c.method === 'POST')
    expect(created).toBeDefined()
    expect(JSON.parse(created?.body ?? '{}')).toEqual({ name: 'Renewals' })
  })
  // ...and the new label is applied to the thread, not merely created.
  await waitFor(() => expect(labelCalls().some((c) => c.method === 'PUT')).toBe(true))
})

// Creation is not offered for a name that already exists — the server would
// resolve it to the existing label anyway, so offering "Create" would lie.
test('an exact existing name (any case) offers no Create item', async () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  openPicker()
  await screen.findByText('Invoices')

  fireEvent.change(screen.getByLabelText(/search or create a label/i), { target: { value: 'invoices' } })

  await waitFor(() => expect(screen.getByText('Invoices')).toBeInTheDocument())
  expect(screen.queryByText(/^Create/)).not.toBeInTheDocument()
})

test('the name field cannot exceed the API length cap', async () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  openPicker()
  const field = await screen.findByLabelText(/search or create a label/i)

  fireEvent.change(field, { target: { value: 'x'.repeat(80) } })

  expect((field as HTMLInputElement).value).toHaveLength(40)
  expect(await screen.findByText(/capped at 40 characters/i)).toBeInTheDocument()
})

test('an empty taxonomy invites creating the first label', async () => {
  labels = []
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  openPicker()

  expect(await screen.findByText(/no labels yet/i)).toBeInTheDocument()
})

test('a failed create is surfaced, not swallowed', async () => {
  createStatus = 500
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  openPicker()
  await screen.findByText('Invoices')

  fireEvent.change(screen.getByLabelText(/search or create a label/i), { target: { value: 'Renewals' } })
  fireEvent.click(await screen.findByText(/Create/))

  expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't update labels/i)
})

test('hitting the workspace label cap explains itself rather than showing a status', async () => {
  createStatus = 422
  renderWithProviders(<LabelPicker threadId="t-1" applied={[]} />)
  openPicker()
  await screen.findByText('Invoices')

  fireEvent.change(screen.getByLabelText(/search or create a label/i), { target: { value: 'Renewals' } })
  fireEvent.click(await screen.findByText(/Create/))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/label limit/i)
  expect(alert).not.toHaveTextContent('422')
})

test('the trigger reports how many labels are applied', () => {
  renderWithProviders(
    <LabelPicker threadId="t-1" applied={[label('l-1', 'Invoices'), label('l-2', 'Partnerships')]} />,
  )
  expect(screen.getByRole('button', { name: /label this thread/i })).toHaveTextContent('2 labels')
})

// The generated client types `labels` as optional (an allOf-composed field
// loses its required marker), so undefined must read as "none".
test('an undefined applied list renders as no labels', () => {
  renderWithProviders(<LabelPicker threadId="t-1" applied={undefined} />)
  expect(screen.getByRole('button', { name: /label this thread/i })).toHaveTextContent('Label')
})

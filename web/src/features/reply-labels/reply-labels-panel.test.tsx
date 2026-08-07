import { fireEvent, screen } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { ReplyLabel } from './api'
import { ReplyLabelsPanel } from './reply-labels-panel'

const jsonHeaders = { 'content-type': 'application/json' }

function label(overrides: Partial<ReplyLabel>): ReplyLabel {
  return {
    id: 'l-custom',
    key: 'wants_demo',
    label: 'Wants a demo',
    color: '#2563EB',
    position: 0,
    is_builtin: false,
    stops_enrollment: false,
    is_automated: false,
    suppresses_contact: false,
    captures_deal: false,
    defers_enrollment: false,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
    ...overrides,
  }
}

const BUILTIN = label({
  id: 'l-builtin',
  key: 'positive',
  label: 'Positive',
  color: '#16A34A',
  is_builtin: true,
  stops_enrollment: true,
  captures_deal: true,
  position: 0,
})
const CUSTOM = label({ position: 1 })

// Per-test responders, keyed by method, for the endpoints the panel hits.
let listResponder: () => Response
let createResponder: () => Response
let deleteResponder: () => Response
let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  listResponder = () =>
    new Response(JSON.stringify({ labels: [BUILTIN, CUSTOM] }), { status: 200, headers: jsonHeaders })
  createResponder = () => new Response(JSON.stringify(CUSTOM), { status: 201, headers: jsonHeaders })
  deleteResponder = () => new Response(null, { status: 204 })

  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
    if (method === 'POST') return createResponder()
    if (method === 'DELETE') return deleteResponder()
    return listResponder()
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renders each label with its key, builtin badge, and role-flag badges', async () => {
  renderWithProviders(<ReplyLabelsPanel />)

  expect(await screen.findByText('Positive')).toBeInTheDocument()
  expect(screen.getByText('Wants a demo')).toBeInTheDocument()
  // Machine keys as muted subtext.
  expect(screen.getByText('positive')).toBeInTheDocument()
  expect(screen.getByText('wants_demo')).toBeInTheDocument()
  // Builtin marker is text, not color/icon alone.
  expect(screen.getByText('Built-in')).toBeInTheDocument()
  // Only the flags that are true render as badges.
  expect(screen.getByText('Stops sequence')).toBeInTheDocument()
  expect(screen.getByText('Captures deal')).toBeInTheDocument()
  expect(screen.queryByText('Defers')).not.toBeInTheDocument()
})

test('the builtin delete button is disabled; the custom one is enabled', async () => {
  renderWithProviders(<ReplyLabelsPanel />)

  const builtinDelete = await screen.findByRole('button', { name: /delete label positive/i })
  expect(builtinDelete).toBeDisabled()
  expect(screen.getByRole('button', { name: /^delete label wants a demo$/i })).toBeEnabled()
})

test('deleting a custom label asks for confirmation and surfaces a 409 in the banner', async () => {
  deleteResponder = () =>
    new Response(JSON.stringify({ error: 'builtin' }), { status: 409, headers: jsonHeaders })

  renderWithProviders(<ReplyLabelsPanel />)
  fireEvent.click(await screen.findByRole('button', { name: /^delete label wants a demo$/i }))

  // Confirm dialog, then the destructive action.
  fireEvent.click(await screen.findByRole('button', { name: /^delete label$/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/built-in labels cannot be deleted/i)
  // The dialog closed so the banner is not hidden underneath it.
  expect(screen.queryByRole('button', { name: /^delete label$/i })).not.toBeInTheDocument()
})

test('a successful delete reports the outcome in the status banner', async () => {
  renderWithProviders(<ReplyLabelsPanel />)
  fireEvent.click(await screen.findByRole('button', { name: /^delete label wants a demo$/i }))
  fireEvent.click(await screen.findByRole('button', { name: /^delete label$/i }))

  // Not findByRole('status'): dnd-kit's hidden drag announcer is also a
  // role=status live region, so query the banner by its text.
  expect(await screen.findByText(/“Wants a demo” was deleted/i)).toBeInTheDocument()
})

/** The "New label" trigger is disabled until the list query settles. */
async function openCreateDialog() {
  await screen.findByText('Positive')
  fireEvent.click(screen.getByRole('button', { name: /new label/i }))
}

test('the create form blocks the automated+stops combination client-side', async () => {
  renderWithProviders(<ReplyLabelsPanel />)
  await openCreateDialog()

  fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'OOO' } })
  fireEvent.click(screen.getByRole('checkbox', { name: /automated mail/i }))
  fireEvent.click(screen.getByRole('checkbox', { name: /stops the sequence/i }))
  fireEvent.click(screen.getByRole('button', { name: /create label/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/automated label cannot also stop the sequence/i)
  // The forbidden combo never reached the server.
  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'POST')).toBe(false)
})

test('the create form blocks defers-without-automated client-side', async () => {
  renderWithProviders(<ReplyLabelsPanel />)
  await openCreateDialog()

  fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Vacation' } })
  fireEvent.click(screen.getByRole('checkbox', { name: /defers the sequence/i }))
  fireEvent.click(screen.getByRole('button', { name: /create label/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/only automated labels can defer/i)
  expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'POST')).toBe(false)
})

test('a 409 duplicate name on create surfaces inline in the dialog', async () => {
  createResponder = () =>
    new Response(JSON.stringify({ error: 'duplicate' }), { status: 409, headers: jsonHeaders })

  renderWithProviders(<ReplyLabelsPanel />)
  await openCreateDialog()

  fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Positive' } })
  fireEvent.click(screen.getByRole('button', { name: /create label/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/a label with that name already exists/i)
  // The dialog stays open so the user can fix the name.
  expect(screen.getByRole('button', { name: /create label/i })).toBeInTheDocument()
})

test('an empty workspace gets the empty state, not a bare list', async () => {
  listResponder = () => new Response(JSON.stringify({ labels: [] }), { status: 200, headers: jsonHeaders })

  renderWithProviders(<ReplyLabelsPanel />)

  expect(await screen.findByText(/no reply labels/i)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /create your first label/i })).toBeInTheDocument()
})

test('a failed list load offers a retry that refetches', async () => {
  listResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<ReplyLabelsPanel />)
  expect(await screen.findByText(/couldn't load reply labels/i)).toBeInTheDocument()

  listResponder = () =>
    new Response(JSON.stringify({ labels: [CUSTOM] }), { status: 200, headers: jsonHeaders })
  fireEvent.click(screen.getByRole('button', { name: /retry/i }))

  expect(await screen.findByText('Wants a demo')).toBeInTheDocument()
})

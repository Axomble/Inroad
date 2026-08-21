import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { CustomFieldDef } from '../api'
import { CustomFieldsPanel } from '../custom-fields-panel'

const jsonHeaders = { 'content-type': 'application/json' }

/** The HTTP methods fetch was called with, however the caller passed them. */
function calledMethods(mock: ReturnType<typeof vi.fn>): string[] {
  return mock.mock.calls.map(([input, init]) => {
    const request = init as RequestInit | undefined
    if (request?.method) return request.method
    return input instanceof Request ? input.method : 'GET'
  })
}

function def(overrides: Partial<CustomFieldDef>): CustomFieldDef {
  return {
    id: 'f-industry',
    key: 'industry',
    label: 'Industry',
    type: 'text',
    options: [],
    created_at: '2026-08-01T00:00:00Z',
    archived: false,
    archived_at: null,
    ...overrides,
  }
}

const LIVE = def({})
const SELECT = def({ id: 'f-tier', key: 'tier', label: 'Tier', type: 'select', options: ['A', 'B'] })
const ARCHIVED = def({
  id: 'f-legacy',
  key: 'legacy',
  label: 'Legacy',
  archived: true,
  archived_at: '2026-08-05T00:00:00Z',
})

let listResponder: () => Response
let archiveResponder: () => Response
let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  listResponder = () =>
    new Response(JSON.stringify([LIVE, SELECT, ARCHIVED]), { status: 200, headers: jsonHeaders })
  archiveResponder = () =>
    new Response(JSON.stringify({ ...ARCHIVED, id: LIVE.id }), { status: 200, headers: jsonHeaders })

  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
    if (method === 'DELETE') return archiveResponder()
    return listResponder()
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('shows each field with the token it maps to, and a select with its options', async () => {
  renderWithProviders(<CustomFieldsPanel />)

  expect(await screen.findByText('Industry')).toBeInTheDocument()
  // The token is the thing an operator copies into a sequence, so it is on screen
  // rather than derivable from the key.
  expect(screen.getByText('{{custom.industry}}')).toBeInTheDocument()
  expect(screen.getByText(/Tier A|A, B/)).toBeInTheDocument()
})

// Archived fields are the explanation for values a contact still shows under a
// key no form offers. Hiding them would make that data look like corruption.
test('lists archived fields separately instead of hiding them', async () => {
  renderWithProviders(<CustomFieldsPanel />)

  expect(await screen.findByText('Legacy')).toBeInTheDocument()
  expect(screen.getByText('Archived')).toBeInTheDocument()
  // An archived row offers no actions — there is nothing to do to it, and a
  // disabled button would only invite the click. Two live fields, two buttons.
  expect(screen.getAllByRole('button', { name: /^archive$/i })).toHaveLength(2)
})

test('archiving explains that values survive and the key stays reserved', async () => {
  renderWithProviders(<CustomFieldsPanel />)

  const archiveButtons = await screen.findAllByRole('button', { name: /^archive$/i })
  fireEvent.click(archiveButtons[0]!)

  expect(await screen.findByText(/Archive “Industry”\?/)).toBeInTheDocument()
  expect(screen.getByText(/keep the values they already hold/i)).toBeInTheDocument()
  expect(screen.getByText(/cannot be reused/i)).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /archive field/i }))
  // fetchBaseQuery may call fetch with a Request or with (url, init) depending
  // on version, so the method is read the same way the mock itself reads it.
  await waitFor(() => expect(calledMethods(fetchMock)).toContain('DELETE'))
  expect(await screen.findByText(/was archived/i)).toBeInTheDocument()
})

test('a failed archive surfaces the error instead of claiming success', async () => {
  archiveResponder = () =>
    new Response(JSON.stringify({ error: 'custom field not found' }), { status: 404, headers: jsonHeaders })
  renderWithProviders(<CustomFieldsPanel />)

  const archiveButtons = await screen.findAllByRole('button', { name: /^archive$/i })
  fireEvent.click(archiveButtons[0]!)
  fireEvent.click(await screen.findByRole('button', { name: /archive field/i }))

  expect(await screen.findByText(/no longer exists/i)).toBeInTheDocument()
})

test('a load failure offers a retry rather than an empty list', async () => {
  listResponder = () => new Response(null, { status: 500 })
  renderWithProviders(<CustomFieldsPanel />)

  expect(await screen.findByText(/Couldn't load custom fields/i)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
})

test('an empty workspace explains what fields are for', async () => {
  listResponder = () => new Response(JSON.stringify([]), { status: 200, headers: jsonHeaders })
  renderWithProviders(<CustomFieldsPanel />)

  expect(await screen.findByText('No custom fields')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /create your first field/i })).toBeInTheDocument()
})

import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { CustomFieldValue } from './api'
import { ContactCustomFields } from './contact-custom-fields'

const jsonHeaders = { 'content-type': 'application/json' }
const CONTACT_ID = 'c-1'

const INDUSTRY: CustomFieldValue = {
  key: 'industry',
  value: 'fintech',
  def: {
    id: 'f-1',
    key: 'industry',
    label: 'Industry',
    type: 'text',
    options: [],
    created_at: '2026-08-01T00:00:00Z',
    archived: false,
    archived_at: null,
  },
}
const TIER: CustomFieldValue = {
  key: 'tier',
  value: '',
  def: {
    id: 'f-2',
    key: 'tier',
    label: 'Tier',
    type: 'select',
    options: ['A', 'B'],
    created_at: '2026-08-01T00:00:00Z',
    archived: false,
    archived_at: null,
  },
}
// A value with no live definition: an archived field, or one written before
// definitions existed.
const ORPHAN: CustomFieldValue = { key: 'legacy', value: 'still sending', def: null }

let getResponder: () => Response
let putResponder: () => Response
let fetchMock: ReturnType<typeof vi.fn>

/** The JSON body of the last PUT, however fetchBaseQuery passed it. */
async function lastPutBody(mock: ReturnType<typeof vi.fn>): Promise<unknown> {
  const put = [...mock.mock.calls].reverse().find(([input, init]) => {
    const request = init as RequestInit | undefined
    return request?.method === 'PUT' || (input instanceof Request && input.method === 'PUT')
  })
  if (!put) return undefined
  const [input, init] = put
  const request = init as RequestInit | undefined
  if (typeof request?.body === 'string') return JSON.parse(request.body)
  return input instanceof Request ? await input.clone().json() : undefined
}

beforeEach(() => {
  getResponder = () =>
    new Response(JSON.stringify([INDUSTRY, TIER, ORPHAN]), { status: 200, headers: jsonHeaders })
  putResponder = () =>
    new Response(JSON.stringify([INDUSTRY, TIER, ORPHAN]), { status: 200, headers: jsonHeaders })

  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
    if (method === 'PUT') return putResponder()
    return getResponder()
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renders an input per live field, typed to the field', async () => {
  renderWithProviders(<ContactCustomFields contactId={CONTACT_ID} />)

  expect(await screen.findByLabelText('Industry')).toHaveValue('fintech')
  // A select renders its options plus an empty choice — "no value" is legal.
  const tier = screen.getByLabelText('Tier')
  expect(tier.tagName).toBe('SELECT')
  expect(screen.getByRole('option', { name: 'A' })).toBeInTheDocument()
})

// The PUT replaces the contact's live field set, so a form that submitted only
// the input it touched would silently clear every other field.
test('submits every live field, not just the edited one', async () => {
  renderWithProviders(<ContactCustomFields contactId={CONTACT_ID} />)

  fireEvent.change(await screen.findByLabelText('Industry'), { target: { value: 'healthcare' } })
  fireEvent.click(screen.getByRole('button', { name: /save fields/i }))

  await waitFor(async () =>
    expect(await lastPutBody(fetchMock)).toEqual({ values: { industry: 'healthcare', tier: '' } }),
  )
})

// Orphaned keys are preserved server-side precisely because the form never
// showed them, so sending one back would be the client asserting something it
// cannot know.
test('never submits a key with no live definition', async () => {
  renderWithProviders(<ContactCustomFields contactId={CONTACT_ID} />)

  fireEvent.click(await screen.findByRole('button', { name: /save fields/i }))

  await waitFor(async () => {
    const body = (await lastPutBody(fetchMock)) as { values: Record<string, string> }
    expect(Object.keys(body.values)).not.toContain('legacy')
  })
})

test('shows orphaned values read-only rather than hiding them', async () => {
  renderWithProviders(<ContactCustomFields contactId={CONTACT_ID} />)

  expect(await screen.findByText('still sending')).toBeInTheDocument()
  expect(screen.getByText(/archived or no longer defined/i)).toBeInTheDocument()
  // Read-only means no input claims it.
  expect(screen.queryByLabelText('legacy')).not.toBeInTheDocument()
})

// A value stored before an option was removed must stay selectable, or the next
// save would silently clear it.
test('keeps a select value that is no longer among the options', async () => {
  getResponder = () =>
    new Response(JSON.stringify([{ ...TIER, value: 'C' }]), { status: 200, headers: jsonHeaders })
  renderWithProviders(<ContactCustomFields contactId={CONTACT_ID} />)

  expect(await screen.findByRole('option', { name: /C \(no longer an option\)/ })).toBeInTheDocument()
  expect(screen.getByLabelText('Tier')).toHaveValue('C')
})

test('a rejected value surfaces the server’s own reason', async () => {
  putResponder = () =>
    new Response(JSON.stringify({ error: 'renewal: value must be a date in YYYY-MM-DD form' }), {
      status: 400,
      headers: jsonHeaders,
    })
  renderWithProviders(<ContactCustomFields contactId={CONTACT_ID} />)

  fireEvent.click(await screen.findByRole('button', { name: /save fields/i }))

  expect(await screen.findByText(/must be a date in YYYY-MM-DD form/)).toBeInTheDocument()
})

test('an empty workspace points at where fields are defined', async () => {
  getResponder = () => new Response(JSON.stringify([]), { status: 200, headers: jsonHeaders })
  renderWithProviders(<ContactCustomFields contactId={CONTACT_ID} />)

  expect(await screen.findByText(/no custom fields yet/i)).toBeInTheDocument()
})

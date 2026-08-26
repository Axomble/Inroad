import { screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ContactContextPanel } from '../contact-context-panel'

// The panel links to contact/company/deal records, so the router's Link needs a
// stand-in; nothing here asserts on navigation.
vi.mock('@tanstack/react-router', async () => {
  const { createElement } = await import('react')
  return {
    Link: ({ children, ...rest }: { children?: unknown }) =>
      createElement('a', { href: '#', ...rest }, children as never),
  }
})

const jsonHeaders = { 'content-type': 'application/json' }

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: jsonHeaders })
}

interface ContactOverrides {
  suppression?: unknown
  deals?: unknown[]
  deal_count?: number
  deals_truncated?: boolean
  job_title?: string
}

let contactStatus: number
let contactOverrides: ContactOverrides
let engagement: Record<string, unknown>
let requestedPaths: string[]

function contactBody(): Record<string, unknown> {
  return {
    id: 'ct-1',
    email: 'jamie@prospect.test',
    first_name: 'Jamie',
    last_name: 'Lin',
    job_title: contactOverrides.job_title ?? 'Head of Ops',
    linkedin_url: '',
    suppression: contactOverrides.suppression ?? null,
    company: { id: 'co-1', name: 'Acme Ltd' },
    deals: contactOverrides.deals ?? [],
    deal_count: contactOverrides.deal_count ?? 0,
    deals_truncated: contactOverrides.deals_truncated ?? false,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  }
}

function deal(id: string, name: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    name,
    pipeline_id: 'p-1',
    stage_id: 's-1',
    stage_label: 'Negotiation',
    stage_color: '#3b82f6',
    stage_is_won: false,
    stage_is_lost: false,
    amount_micros: 25_000_000_000,
    currency: 'USD',
    close_date: null,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
    ...extra,
  }
}

beforeEach(() => {
  contactStatus = 200
  contactOverrides = {}
  engagement = {
    contact_id: 'ct-1',
    emails_sent: 4,
    opens_indicative: 3,
    clicks: 1,
    replies: 2,
    bounces: 0,
    unsubscribes: 0,
    open_rate: 0.75,
    click_rate: 0.25,
    campaigns_enrolled: 1,
    opens_measurable: true,
    last_activity_at: '2026-08-20T10:00:00Z',
    campaigns: [],
    campaigns_truncated: false,
  }
  requestedPaths = []

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      requestedPaths.push(`${method} ${url.pathname}${url.search}`)

      if (url.pathname.endsWith('/engagement')) return json(engagement)
      if (/\/contacts\/[^/]+$/.test(url.pathname)) {
        if (contactStatus !== 200) return json({ error: 'nope' }, contactStatus)
        return json(contactBody())
      }
      // The records panels fetch their own notes/tasks.
      if (url.pathname.includes('/crm/notes')) return json({ items: [] })
      if (url.pathname.includes('/crm/tasks')) return json({ items: [] })
      return json({ error: 'unhandled' }, 404)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renders the contact identity, company and role', async () => {
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByText('Jamie Lin')).toBeInTheDocument()
  expect(screen.getByText('jamie@prospect.test')).toBeInTheDocument()
  expect(screen.getByText('Head of Ops')).toBeInTheDocument()
  expect(screen.getByText('Acme Ltd')).toBeInTheDocument()
})

// A legacy direct-send match has no contact. That is not an error — nothing is
// wrong, there is simply no record behind the reply.
test('a thread with no contact explains itself and fetches nothing', async () => {
  renderWithProviders(<ContactContextPanel contactId={null} />)

  expect(await screen.findByText(/isn't linked to a contact/i)).toBeInTheDocument()
  // skipToken must mean no request at all, not a request for `null`.
  expect(requestedPaths.filter((p) => p.includes('/contacts/'))).toHaveLength(0)
})

test('a deleted contact is explained rather than shown as a failure', async () => {
  contactStatus = 404
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByText(/has been deleted/i)).toBeInTheDocument()
})

test('a failed load surfaces a message, not a blank rail', async () => {
  contactStatus = 500
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByRole('status')).toBeInTheDocument()
})

test('engagement counters come from the API', async () => {
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  await screen.findByText('Jamie Lin')
  const sent = await screen.findByText('Sent')
  // The counter sits in the same <div> as its label.
  expect(sent.parentElement).toHaveTextContent('4')
})

// With tracking off, zero opens is an absence of MEASUREMENT, not of interest.
// Saying "0 opens" without that caveat would be a wrong conclusion.
test('unmeasurable opens are called out', async () => {
  engagement = { ...engagement, opens_indicative: 0, opens_measurable: false }
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByText(/open tracking is off/i)).toBeInTheDocument()
})

test('measurable opens carry no caveat', async () => {
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  await screen.findByText('Jamie Lin')
  expect(screen.queryByText(/open tracking is off/i)).not.toBeInTheDocument()
})

test('bounces and unsubscribes are reported', async () => {
  engagement = { ...engagement, bounces: 2, unsubscribes: 1 }
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByText(/2 bounced/)).toBeInTheDocument()
  expect(screen.getByText(/1 unsubscribed/)).toBeInTheDocument()
})

test('a contact with nothing sent says so rather than showing four zeroes', async () => {
  engagement = { ...engagement, emails_sent: 0, opens_indicative: 0, clicks: 0, replies: 0 }
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByText(/nothing sent to this contact yet/i)).toBeInTheDocument()
})

test('deals are listed with their stage and amount', async () => {
  contactOverrides = { deals: [deal('d-1', 'Acme renewal')], deal_count: 1 }
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByText('Acme renewal')).toBeInTheDocument()
  expect(screen.getByText('Negotiation')).toBeInTheDocument()
  expect(screen.getByRole('heading', { name: '1 deal' })).toBeInTheDocument()
})

test('a won deal is marked as won by text, not colour alone', async () => {
  contactOverrides = {
    deals: [deal('d-1', 'Closed one', { stage_is_won: true, stage_label: 'Won' })],
    deal_count: 1,
  }
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByText('Won', { selector: 'span.text-ok' })).toBeInTheDocument()
})

// The API caps the embedded deal list; the rail must not imply it is complete.
test('a truncated deal list says so', async () => {
  contactOverrides = {
    deals: [deal('d-1', 'One of many')],
    deal_count: 40,
    deals_truncated: true,
  }
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  await screen.findByText('One of many')
  expect(screen.getByRole('heading', { name: '40 deals' })).toBeInTheDocument()
  expect(screen.getByText(/showing/i)).toBeInTheDocument()
})

test('no deals reads as empty, not as broken', async () => {
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByText('No deals yet.')).toBeInTheDocument()
})

// Suppression is the one fact that changes what the operator may DO, so it is
// called out — and the primary/alias distinction matters.
test('a suppressed primary address warns that replies will not send', async () => {
  contactOverrides = { suppression: { reason: 'unsubscribed', is_primary_email: true } }
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  expect(await screen.findByText(/will not send/i)).toBeInTheDocument()
})

test('a suppressed alias is distinguished from a suppressed contact', async () => {
  contactOverrides = { suppression: { reason: 'bounced', is_primary_email: false } }
  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  const notice = await screen.findByText(/alias of this contact is suppressed/i)
  expect(notice).toBeInTheDocument()
  expect(screen.queryByText(/will not send/i)).not.toBeInTheDocument()
})

test('an unnamed contact falls back to its email as the display name', async () => {
  const fetchMock = vi.mocked(globalThis.fetch)
  fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
    const href = input instanceof Request ? input.url : typeof input === 'string' ? input : (input as URL).href
    const url = new URL(href, 'http://localhost')
    if (url.pathname.endsWith('/engagement')) return json(engagement)
    if (/\/contacts\/[^/]+$/.test(url.pathname)) {
      return json({ ...contactBody(), first_name: '', last_name: '' })
    }
    if (url.pathname.includes('/crm/')) return json({ items: [] })
    return json({ error: 'unhandled' }, 404)
  })

  renderWithProviders(<ContactContextPanel contactId="ct-1" />)

  // The email appears as the heading link as well as the secondary line.
  await waitFor(() => expect(screen.getAllByText('jamie@prospect.test').length).toBeGreaterThan(1))
})

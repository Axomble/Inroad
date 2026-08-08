import { fireEvent, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ContactDetailPage } from './contact-detail-page'
import type { ContactDetail, ContactEngagement } from './api'

// The contact record answers two questions in order: may we email this person,
// and what have they done with the mail we sent. The first outranks everything
// else on the page, and both have to stay legible when the other half fails —
// they are two requests on purpose.

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, ...props }: { children: React.ReactNode; to?: string; params?: unknown }) => {
    const { to, params, ...rest } = props as Record<string, unknown>
    const id = (params as { id?: string } | undefined)?.id
    const href = typeof to === 'string' ? (id ? to.replace('$id', id) : to) : '#'
    return <a href={href} {...rest}>{children}</a>
  },
}))

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

const contact = (overrides: Partial<ContactDetail> = {}): ContactDetail => ({
  id: 'c-1',
  email: 'dana@acme.test',
  first_name: 'Dana',
  last_name: 'Reed',
  job_title: 'Head of Ops',
  linkedin_url: 'https://linkedin.example/dana',
  suppression: null,
  company: { id: 'co-1', name: 'Acme', domain: 'acme.test' },
  deals: [
    {
      id: 'd-1',
      name: 'Acme renewal',
      pipeline_id: 'p-1',
      stage_id: 's-1',
      stage_label: 'Qualified',
      stage_color: '#3B82F6',
      stage_is_won: false,
      stage_is_lost: false,
      amount_micros: 1_000_000_000,
      currency: 'USD',
      close_date: null,
      created_at: '2026-08-01T00:00:00Z',
      updated_at: '2026-08-01T00:00:00Z',
    },
  ],
  deals_truncated: false,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...overrides,
})

const engagement = (overrides: Partial<ContactEngagement> = {}): ContactEngagement => ({
  contact_id: 'c-1',
  emails_sent: 8,
  opens_indicative: 4,
  clicks: 2,
  replies: 1,
  bounces: 0,
  unsubscribes: 0,
  open_rate: 0.5,
  click_rate: 0.25,
  campaigns_enrolled: 1,
  last_activity_at: '2026-08-05T09:15:00Z',
  campaigns: [
    {
      campaign_id: 'ca-1',
      campaign_name: 'Q3 outbound',
      status: 'stopped',
      current_step: 3,
      stop_reason: 'replied',
      enrolled_at: '2026-07-01T00:00:00Z',
      last_sent_at: '2026-08-04T00:00:00Z',
    },
  ],
  campaigns_truncated: false,
  ...overrides,
})

let contactResponse: () => Response
let engagementResponse: () => Response

beforeEach(() => {
  contactResponse = () => json(contact())
  engagementResponse = () => json(engagement())

  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = new URL(input instanceof Request ? input.url : String(input))
      const { pathname } = url
      if (pathname.endsWith('/contacts/c-1/engagement')) return Promise.resolve(engagementResponse())
      if (pathname.endsWith('/contacts/c-1')) return Promise.resolve(contactResponse())
      if (pathname.endsWith('/crm/notes')) return Promise.resolve(json({ items: [] }))
      if (pathname.endsWith('/crm/tasks')) return Promise.resolve(json({ items: [] }))
      if (pathname.endsWith('/crm/events')) return Promise.resolve(json({ items: [] }))
      throw new Error(`unexpected request: ${pathname}`)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** Waits for a panel's heading, then scopes queries to that panel. */
async function panel(name: string | RegExp): Promise<HTMLElement> {
  const heading = await screen.findByRole('heading', { name })
  const section = heading.closest('section')
  if (!section) throw new Error(`no panel named ${String(name)}`)
  return section
}

test('a contact who may be emailed carries no suppression warning', async () => {
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  expect(await screen.findByRole('heading', { name: 'Details' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: /do not email/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: /suppressed/i })).not.toBeInTheDocument()
})

test('a suppressed primary address says plainly that this person must not be emailed', async () => {
  contactResponse = () =>
    json(contact({
      suppression: { reason: 'complaint', email: 'dana@acme.test', is_primary_email: true, since: '2026-07-04T00:00:00Z' },
    }))
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  const notice = await panel(/do not email this contact/i)
  // The reason is in words, and a complaint is never softened into "unsubscribed".
  expect(within(notice).getByText(/reported a message as spam/i)).toBeInTheDocument()
  expect(within(notice).queryByText(/unsubscrib/i)).not.toBeInTheDocument()
  // Which address, and that sending is actually blocked.
  expect(within(notice).getByText('dana@acme.test')).toBeInTheDocument()
  expect(within(notice).getByText(/campaigns will skip this contact/i)).toBeInTheDocument()
})

test('a suppressed secondary address is a warning, not a block, and reads differently', async () => {
  contactResponse = () =>
    json(contact({
      suppression: { reason: 'bounce', email: 'old@acme.test', is_primary_email: false, since: '2026-07-04T00:00:00Z' },
    }))
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  const notice = await panel(/one of this contact’s addresses is suppressed/i)
  expect(within(notice).getByText('old@acme.test')).toBeInTheDocument()
  expect(within(notice).getByText(/primary address is still deliverable/i)).toBeInTheDocument()
  // Explicitly not the hard-stop wording.
  expect(screen.queryByRole('heading', { name: /do not email this contact/i })).not.toBeInTheDocument()
})

test('engagement shows lifetime counts, with opens hedged rather than stated flatly', async () => {
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  // The panel's heading renders before its data — it owns its own loading state —
  // so the first assertion has to wait for the numbers to land.
  const engagementPanel = await panel('Email engagement')
  expect(await within(engagementPanel).findByText('Opens (indicative)')).toBeInTheDocument()
  expect(within(engagementPanel).queryByText(/^Opens$/)).not.toBeInTheDocument()
  expect(within(engagementPanel).getByText(/prefetch images/i)).toBeInTheDocument()
  // 0..1 fractions arrive from the API and are rendered as percentages here.
  expect(within(engagementPanel).getByText('50% of sent')).toBeInTheDocument()
  expect(within(engagementPanel).getByText('25% of sent')).toBeInTheDocument()
})

test('an enrollment explains why the sequence stopped, which is often the question', async () => {
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  const engagementPanel = await panel('Email engagement')
  expect(await within(engagementPanel).findByRole('link', { name: 'Q3 outbound' })).toHaveAttribute('href', '/app/campaigns/ca-1')
  expect(within(engagementPanel).getByText(/step 3 was the last sent/i)).toBeInTheDocument()
  expect(within(engagementPanel).getByText(/stopped: they replied/i)).toBeInTheDocument()
})

test('a contact never sent to gets facts, not zeroes dressed as rates', async () => {
  engagementResponse = () =>
    json(engagement({
      emails_sent: 0,
      opens_indicative: 0,
      clicks: 0,
      replies: 0,
      open_rate: 0,
      click_rate: 0,
      campaigns_enrolled: 0,
      last_activity_at: null,
      campaigns: [],
    }))
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  const engagementPanel = await panel('Email engagement')
  expect(await within(engagementPanel).findByText('Nothing yet')).toBeInTheDocument()
  expect(within(engagementPanel).getByText(/never been enrolled in a campaign/i)).toBeInTheDocument()
  // A rate over zero sends is meaningless, so it isn't shown at all.
  expect(within(engagementPanel).queryByText(/of sent/)).not.toBeInTheDocument()
})

test('a failed engagement read leaves the rest of the record usable', async () => {
  engagementResponse = () => json({ error: 'boom' }, 500)
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('The server had a problem loading CRM data.')
  // The cheap half of the page rendered regardless — the two are separate requests.
  expect(screen.getByRole('link', { name: 'Acme' })).toHaveAttribute('href', '/app/companies/co-1')
  expect(screen.getByRole('link', { name: 'Acme renewal' })).toHaveAttribute('href', '/app/deals/d-1')

  engagementResponse = () => json(engagement())
  fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))
  expect(await screen.findByText('Opens (indicative)')).toBeInTheDocument()
})

test('the record links out to the company and to each deal', async () => {
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  expect(await screen.findByRole('link', { name: 'Acme' })).toHaveAttribute('href', '/app/companies/co-1')
  expect(screen.getByRole('link', { name: 'Acme renewal' })).toHaveAttribute('href', '/app/deals/d-1')
  expect(within(await panel('Deals')).getByText('Qualified')).toBeInTheDocument()
})

test('a capped deal list admits it was capped', async () => {
  contactResponse = () => json(contact({ deals_truncated: true }))
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  expect(await screen.findByText(/showing the first 1 deals in board order/i)).toBeInTheDocument()
})

test('an unlinked contact with no deals states both, rather than looking broken', async () => {
  contactResponse = () => json(contact({ company: null, deals: [], job_title: '', linkedin_url: '' }))
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  expect(await screen.findByText('No deals name this contact yet.')).toBeInTheDocument()
  expect(within(await panel('Details')).getByText('Not linked')).toBeInTheDocument()
  expect(within(await panel('Details')).getAllByText('Not set')).toHaveLength(2)
})

test('a deleted contact reads as missing, without hinting the id is real', async () => {
  contactResponse = () => json({ error: 'not found' }, 404)
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  expect(await screen.findByText('Contact not found')).toBeInTheDocument()
  // The API answers 404 for another workspace's contact too, deliberately, so the
  // copy must not imply the record exists somewhere.
  expect(screen.queryByText(/access/i)).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: /back to contacts/i })).toHaveAttribute('href', '/app/contacts')
})

test('a failed contact read offers a retry instead of claiming the contact is gone', async () => {
  contactResponse = () => json({ error: 'boom' }, 500)
  renderWithProviders(<ContactDetailPage contactId="c-1" />)

  expect(await screen.findByText('This contact could not be loaded')).toBeInTheDocument()
  expect(screen.queryByText('Contact not found')).not.toBeInTheDocument()

  contactResponse = () => json(contact())
  fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
  expect(await screen.findByRole('heading', { name: 'Details' })).toBeInTheDocument()
})

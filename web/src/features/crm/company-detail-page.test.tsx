import { fireEvent, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { CompanyDetailPage } from './company-detail-page'
import type { CrmCompany, CrmCompanyContact, CrmDeal } from './api'

// A company record earns its keep by being a hub: from here you reach the people
// at the account and the deals on it. Both are paginated sub-resources, so the
// page has to distinguish an empty roster from a failed read from a capped page,
// and one failing must not take the other down.

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

const company = (overrides: Partial<CrmCompany> = {}): CrmCompany => ({
  id: 'co-1',
  name: 'Acme',
  domain: 'acme.test',
  currency: 'USD',
  deal_count: 1,
  annual_revenue_micros: 5_000_000_000,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...overrides,
})

const contact = (overrides: Partial<CrmCompanyContact> = {}): CrmCompanyContact => ({
  id: 'c-1',
  email: 'dana@acme.test',
  first_name: 'Dana',
  last_name: 'Reed',
  job_title: 'Head of Ops',
  linkedin_url: '',
  created_at: '2026-08-01T00:00:00Z',
  ...overrides,
})

const deal = (overrides: Partial<CrmDeal> = {}): CrmDeal => ({
  id: 'd-1',
  pipeline_id: 'p-1',
  stage_id: 's-1',
  company_id: 'co-1',
  name: 'Acme renewal',
  currency: 'USD',
  amount_micros: 1_000_000_000,
  position: 1,
  source: 'manual',
  created_by_actor: { type: 'user' },
  pipeline_name: 'Sales',
  stage_label: 'Qualified',
  stage_color: '#888888',
  stage_is_won: false,
  stage_is_lost: false,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...overrides,
})

let companyResponse: () => Response
let contactsResponse: () => Response
let dealsResponse: () => Response
let listRequests: URL[]

beforeEach(() => {
  companyResponse = () => json(company())
  contactsResponse = () => json({ items: [contact()] })
  dealsResponse = () => json({ items: [deal()] })
  listRequests = []

  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = new URL(input instanceof Request ? input.url : String(input))
      const { pathname } = url
      if (pathname.endsWith('/crm/companies/co-1/contacts')) {
        listRequests.push(url)
        return Promise.resolve(contactsResponse())
      }
      if (pathname.endsWith('/crm/companies/co-1/deals')) {
        listRequests.push(url)
        return Promise.resolve(dealsResponse())
      }
      if (pathname.endsWith('/crm/companies/co-1')) return Promise.resolve(companyResponse())
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

function panel(name: string): HTMLElement {
  const heading = screen.getByRole('heading', { name })
  const section = heading.closest('section')
  if (!section) throw new Error(`no panel named ${name}`)
  return section
}

test('the account reaches its people and its deals, each linked to their record', async () => {
  renderWithProviders(<CompanyDetailPage companyId="co-1" />)

  expect(await screen.findByRole('link', { name: 'Dana Reed' })).toHaveAttribute('href', '/app/contacts/c-1')
  expect(within(panel('People')).getByText('dana@acme.test')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Acme renewal' })).toHaveAttribute('href', '/app/deals/d-1')
  // The stage carries a colour, but the label is what states it.
  expect(within(panel('Deals')).getByText('Qualified')).toBeInTheDocument()
})

test('a contact with no name on file is still reachable by their address', async () => {
  contactsResponse = () => json({ items: [contact({ first_name: '', last_name: '', job_title: '' })] })
  renderWithProviders(<CompanyDetailPage companyId="co-1" />)

  expect(await screen.findByRole('link', { name: 'dana@acme.test' })).toHaveAttribute('href', '/app/contacts/c-1')
  expect(within(panel('People')).getByText('No name on file')).toBeInTheDocument()
})

test('both sub-resources ask for a full page and admit when one was capped', async () => {
  contactsResponse = () => json({ items: [contact()], next_cursor: 'opaque-cursor' })
  renderWithProviders(<CompanyDetailPage companyId="co-1" />)

  expect(await screen.findByText(/showing the first 1 contacts at this company/i)).toBeInTheDocument()
  expect(listRequests.every((url) => url.searchParams.get('limit') === '200')).toBe(true)
  // The deals page was complete, so it claims nothing about paging.
  expect(screen.queryByText(/deals on this account\. More exist/i)).not.toBeInTheDocument()
})

test('an empty roster and an empty pipeline both say so rather than looking broken', async () => {
  contactsResponse = () => json({ items: [] })
  dealsResponse = () => json({ items: [] })
  renderWithProviders(<CompanyDetailPage companyId="co-1" />)

  expect(await screen.findByText('No contacts are linked to this company yet.')).toBeInTheDocument()
  expect(screen.getByText('No deals on this account yet.')).toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

test('a failed roster read is reported as a failure and does not take the deals with it', async () => {
  contactsResponse = () => json({ error: 'boom' }, 500)
  renderWithProviders(<CompanyDetailPage companyId="co-1" />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('The server had a problem loading CRM data.')
  expect(screen.queryByText('No contacts are linked to this company yet.')).not.toBeInTheDocument()
  // The other panel is a separate request and rendered fine.
  expect(screen.getByRole('link', { name: 'Acme renewal' })).toBeInTheDocument()

  contactsResponse = () => json({ items: [contact()] })
  fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))
  expect(await screen.findByRole('link', { name: 'Dana Reed' })).toBeInTheDocument()
})

test('a deleted company reads as missing, with a way back to the list', async () => {
  companyResponse = () => json({ error: 'not found' }, 404)
  renderWithProviders(<CompanyDetailPage companyId="co-1" />)

  expect(await screen.findByText('Company not found')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /back to companies/i })).toHaveAttribute('href', '/app/companies')
})

test('a failed company read offers a retry instead of claiming the company is gone', async () => {
  companyResponse = () => json({ error: 'boom' }, 500)
  renderWithProviders(<CompanyDetailPage companyId="co-1" />)

  expect(await screen.findByText('This company could not be loaded')).toBeInTheDocument()
  expect(screen.queryByText('Company not found')).not.toBeInTheDocument()

  companyResponse = () => json(company())
  fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
  expect(await screen.findByText('Account details')).toBeInTheDocument()
})

test('an unset revenue is stated as unset rather than rendered as zero', async () => {
  companyResponse = () => json(company({ annual_revenue_micros: null }))
  renderWithProviders(<CompanyDetailPage companyId="co-1" />)

  expect(await screen.findByText('Not set')).toBeInTheDocument()
})

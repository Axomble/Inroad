import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { CompaniesPage } from '../companies-page'
import type { CrmCompany } from '../api'

// Companies is the account layer contacts and deals hang off, so the row has to
// reach the record, a capped page has to admit it is capped, and a failure has to
// read as a failure rather than as an empty workspace.

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
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...overrides,
})

const money = (amount: number, currency = 'USD') =>
  new Intl.NumberFormat(undefined, { style: 'currency', currency, maximumFractionDigits: 0 }).format(amount)

let companiesResponse: () => Response
let createResponse: () => Response
let companyPosts: Record<string, unknown>[]
let listRequests: URL[]

beforeEach(() => {
  companiesResponse = () => json({ items: [company()] })
  createResponse = () => json(company({ id: 'co-new' }), 201)
  companyPosts = []
  listRequests = []

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : new Request(input)
      const url = new URL(request.url)
      if (!url.pathname.endsWith('/crm/companies')) throw new Error(`unexpected request: ${url.pathname}`)
      if (request.method === 'POST') {
        companyPosts.push(JSON.parse(await request.text()) as Record<string, unknown>)
        return createResponse()
      }
      listRequests.push(url)
      return companiesResponse()
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

async function renderPage() {
  const view = renderWithProviders(<CompaniesPage />)
  await waitFor(() => expect(screen.queryByLabelText('Loading companies')).not.toBeInTheDocument())
  return view
}

function statValue(label: string): string {
  const stat = screen.getAllByText(label).map((node) => node.closest('[data-slot="stat"]')).find(Boolean)
  if (!stat) throw new Error(`no stat named ${label}`)
  return stat.textContent ?? ''
}

test('each row opens the company record, so the account is a hub and not a dead end', async () => {
  await renderPage()

  expect(screen.getByRole('link', { name: 'Acme' })).toHaveAttribute('href', '/app/companies/co-1')
})

test('company revenue converts to micros and an unset revenue is omitted', async () => {
  await renderPage()
  fireEvent.click(screen.getByRole('button', { name: /new company/i }))
  await screen.findByLabelText('Company name')

  fireEvent.change(screen.getByLabelText('Company name'), { target: { value: 'Globex' } })
  fireEvent.change(screen.getByLabelText('Domain'), { target: { value: 'globex.test' } })
  fireEvent.change(screen.getByLabelText('Annual revenue'), { target: { value: '2.5' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  await waitFor(() => expect(companyPosts).toHaveLength(1))
  expect(companyPosts[0]).toEqual({ name: 'Globex', domain: 'globex.test', currency: 'USD', annual_revenue_micros: 2_500_000 })
})

test('rejects a currency that is not three letters before it reaches the server', async () => {
  await renderPage()
  fireEvent.click(screen.getByRole('button', { name: /new company/i }))
  await screen.findByLabelText('Company name')

  fireEvent.change(screen.getByLabelText('Company name'), { target: { value: 'Globex' } })
  fireEvent.change(screen.getByLabelText('Currency'), { target: { value: '12a' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  expect(await screen.findByText('Use a three-letter currency code')).toBeInTheDocument()
  expect(companyPosts).toHaveLength(0)
})

test("surfaces the server's own reason when a create is rejected, keeping the values entered", async () => {
  createResponse = () => json({ error: 'a company with that domain already exists' }, 409)
  await renderPage()
  fireEvent.click(screen.getByRole('button', { name: /new company/i }))
  await screen.findByLabelText('Company name')

  fireEvent.change(screen.getByLabelText('Company name'), { target: { value: 'Acme' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  expect(await screen.findByText('a company with that domain already exists')).toBeInTheDocument()
  expect(screen.getByLabelText('Company name')).toHaveValue('Acme')
})

test('a zero revenue is shown as zero, not as "not set"', async () => {
  companiesResponse = () => json({ items: [company({ annual_revenue_micros: 0 })] })
  await renderPage()

  expect(screen.getByText(money(0))).toBeInTheDocument()
  expect(screen.queryByText('Revenue not set')).not.toBeInTheDocument()
})

test('an empty workspace says what to do instead of rendering a blank page', async () => {
  companiesResponse = () => json({ items: [] })
  await renderPage()

  expect(screen.getByText('No companies yet')).toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

test('a missing CRM scope reads as a permission problem, and retry re-requests the list', async () => {
  companiesResponse = () => json({ error: 'forbidden' }, 403)
  renderWithProviders(<CompaniesPage />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('crm:read')
  // A failure is not an empty workspace.
  expect(screen.queryByText('No companies yet')).not.toBeInTheDocument()
  expect(statValue('Companies')).toContain('—')

  companiesResponse = () => json({ items: [company()] })
  fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))

  expect(await screen.findByRole('link', { name: 'Acme' })).toBeInTheDocument()
})

test('asks for a full page and says so when the server held records back', async () => {
  companiesResponse = () => json({ items: [company()], next_cursor: 'opaque-cursor' })
  await renderPage()

  expect(listRequests[0]?.searchParams.get('limit')).toBe('200')
  expect(screen.getByRole('status')).toHaveTextContent('Showing the first 1 companies')
  // A partial list must not present its counts as the whole workspace.
  expect(statValue('Companies')).toContain('1+')
})

test('a complete list says nothing about paging', async () => {
  await renderPage()

  expect(screen.queryByRole('status')).not.toBeInTheDocument()
  expect(statValue('Companies')).not.toContain('+')
})

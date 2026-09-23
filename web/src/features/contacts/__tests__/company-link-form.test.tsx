import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { CompanyLinkForm } from '../company-link-form'
import type { CrmCompany } from '@/features/crm/api'

// This form is the only thing in the product that writes `contacts.company_id`.
// Its risks are in two places: the request body (an omitted `company_id` is a
// 400, and an unlink has to be an explicit null) and reach — a picker that can
// only show one page of companies cannot link a contact to the rest.

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

const company = (id: string, name: string): CrmCompany => ({
  id,
  name,
  domain: `${name.toLowerCase().replace(/\s+/g, '')}.test`,
  currency: 'USD',
  deal_count: 0,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
})

/** Answers GET /crm/companies from its query string. */
let companiesResponse: (params: URLSearchParams) => Response
let linkResponse: () => Response
let puts: { url: string; body: unknown }[]
let companyRequests: URLSearchParams[]

beforeEach(() => {
  companiesResponse = () => json({ items: [company('co-1', 'Acme'), company('co-2', 'Globex')] })
  linkResponse = () => json({ id: 'c-1' })
  puts = []
  companyRequests = []

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : new Request(input)
      const url = new URL(request.url)
      if (url.pathname.endsWith('/contacts/c-1/company')) {
        puts.push({ url: url.pathname, body: JSON.parse(await request.text()) as unknown })
        return linkResponse()
      }
      if (url.pathname.endsWith('/crm/companies')) {
        companyRequests.push(url.searchParams)
        return companiesResponse(url.searchParams)
      }
      throw new Error(`unexpected request: ${request.method} ${url.pathname}`)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const linked = { id: 'co-1', name: 'Acme', domain: 'acme.test' }

async function openPicker(name: RegExp) {
  fireEvent.click(screen.getByRole('button', { name }))
  return screen.findByRole('combobox', { name: 'Company' })
}

function save() {
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
}

test('an unlinked contact says so and offers to link, not to "change"', () => {
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)

  expect(screen.getByText('Not linked')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /link this contact to a company/i })).toBeInTheDocument()
})

test('a linked contact shows the company as a link to its record', () => {
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={linked} />)

  expect(screen.getByRole('link', { name: 'Acme' })).toHaveAttribute('href', '/app/companies/co-1')
  expect(screen.getByRole('button', { name: /change the company/i })).toBeInTheDocument()
})

test('linking sends the chosen company id', async () => {
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  await openPicker(/link this contact/i)

  fireEvent.click(await screen.findByRole('option', { name: /Globex/ }))
  save()

  await waitFor(() => expect(puts).toHaveLength(1))
  expect(puts[0]).toEqual({ url: '/api/v1/contacts/c-1/company', body: { company_id: 'co-2' } })
})

test('typing searches the server once the user pauses, not per keystroke', async () => {
  companiesResponse = (params) =>
    params.get('q') === 'initech'
      ? json({ items: [company('co-7', 'Initech')] })
      : json({ items: [company('co-1', 'Acme')] })
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  const input = await openPicker(/link this contact/i)
  await screen.findByRole('option', { name: /Acme/ })

  for (const typed of ['i', 'in', 'ini', 'initech']) fireEvent.change(input, { target: { value: typed } })

  expect(await screen.findByRole('option', { name: /Initech/ })).toBeInTheDocument()
  expect(screen.queryByRole('option', { name: /Acme/ })).not.toBeInTheDocument()
  const searched = companyRequests.map((params) => params.get('q'))
  expect(searched).toEqual([null, 'initech'])
})

test('one character browses instead of searching, and says why', async () => {
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  const input = await openPicker(/link this contact/i)
  await screen.findByRole('option', { name: /Acme/ })

  fireEvent.change(input, { target: { value: 'a' } })

  expect(input).toHaveAccessibleDescription(/at least 2 characters/i)
  // Wait past the debounce: a one-character q would be a 422, so none is sent.
  await new Promise((resolve) => setTimeout(resolve, 400))
  expect(companyRequests.every((params) => !params.has('q'))).toBe(true)
})

test('a search with no matches says so', async () => {
  companiesResponse = (params) => (params.has('q') ? json({ items: [] }) : json({ items: [company('co-1', 'Acme')] }))
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  const input = await openPicker(/link this contact/i)
  await screen.findByRole('option', { name: /Acme/ })

  fireEvent.change(input, { target: { value: 'zzz' } })

  expect(await screen.findByText('No company matches “zzz”.')).toBeInTheDocument()
})

test('load more fetches the next page by cursor and appends it', async () => {
  companiesResponse = (params) =>
    params.get('cursor') === 'page-2'
      ? json({ items: [company('co-201', 'Zenith')] })
      : json({ items: [company('co-1', 'Acme')], next_cursor: 'page-2' })
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  await openPicker(/link this contact/i)
  await screen.findByRole('option', { name: /Acme/ })

  fireEvent.click(screen.getByRole('button', { name: 'Load more' }))

  fireEvent.click(await screen.findByRole('option', { name: /Zenith/ }))
  expect(screen.getByRole('option', { name: /Acme/ })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
  save()
  await waitFor(() => expect(puts).toHaveLength(1))
  expect(puts[0]?.body).toEqual({ company_id: 'co-201' })
})

test('a failed next page is reported without losing the rows already loaded', async () => {
  companiesResponse = (params) =>
    params.has('cursor') ? json({ error: 'boom' }, 500) : json({ items: [company('co-1', 'Acme')], next_cursor: 'page-2' })
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  await openPicker(/link this contact/i)
  await screen.findByRole('option', { name: /Acme/ })

  fireEvent.click(screen.getByRole('button', { name: 'Load more' }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/more companies could not be loaded/i)
  expect(screen.getByRole('option', { name: /Acme/ })).toBeInTheDocument()
})

test('the currently linked company stays selected even when no loaded page contains it', async () => {
  companiesResponse = () => json({ items: [company('co-9', 'Someone else')], next_cursor: 'more' })
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={linked} />)
  await openPicker(/change the company/i)
  await screen.findByRole('option', { name: /Someone else/ })

  // Shown as the selection, not smuggled into the result list.
  expect(screen.getByText('Acme')).toBeInTheDocument()
  expect(screen.queryByRole('option', { name: /Acme/ })).not.toBeInTheDocument()

  // Saving untouched keeps the link rather than silently proposing an unlink.
  save()
  await waitFor(() => expect(puts).toHaveLength(1))
  expect(puts[0]?.body).toEqual({ company_id: 'co-1' })
})

test('unlinking sends an explicit null, never an omitted field', async () => {
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={linked} />)
  await openPicker(/change the company/i)

  fireEvent.click(screen.getByRole('button', { name: 'Clear the selected company' }))
  expect(screen.getByText('No company')).toBeInTheDocument()
  save()

  await waitFor(() => expect(puts).toHaveLength(1))
  // The API rejects `{}` with a 400 on purpose, so that an absent field can never
  // be read as "detach". The body must carry the null.
  expect(puts[0]?.body).toEqual({ company_id: null })
  expect(Object.keys(puts[0]?.body as object)).toContain('company_id')
})

test('a missing company and a missing contact read differently, because the fix differs', async () => {
  linkResponse = () => json({ error: 'company not found' }, 404)
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  await openPicker(/link this contact/i)
  fireEvent.click(await screen.findByRole('option', { name: /Acme/ }))
  save()

  expect(await screen.findByRole('alert')).toHaveTextContent(/that company no longer exists/i)

  linkResponse = () => json({ error: 'contact not found' }, 404)
  save()

  expect(await screen.findByText(/this contact no longer exists/i)).toBeInTheDocument()
})

test('the form stays open on failure so the choice is not lost', async () => {
  linkResponse = () => json({ error: 'boom' }, 500)
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  await openPicker(/link this contact/i)
  fireEvent.click(await screen.findByRole('option', { name: /Acme/ }))
  save()

  expect(await screen.findByRole('alert')).toHaveTextContent('The server had a problem.')
  expect(screen.getByRole('option', { name: /Acme/ })).toHaveAttribute('aria-selected', 'true')
})

test('a failed company list explains why there is nothing to choose from, and can be retried', async () => {
  let calls = 0
  companiesResponse = () => {
    calls += 1
    return calls === 1 ? json({ error: 'boom' }, 500) : json({ items: [company('co-1', 'Acme')] })
  }
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  await openPicker(/link this contact/i)

  expect(await screen.findByRole('alert')).toHaveTextContent(/company list could not be loaded/i)
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

  expect(await screen.findByRole('option', { name: /Acme/ })).toBeInTheDocument()
})

test('cancelling closes the form without writing', async () => {
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={linked} />)
  await openPicker(/change the company/i)

  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

  expect(await screen.findByRole('link', { name: 'Acme' })).toBeInTheDocument()
  expect(puts).toHaveLength(0)
})

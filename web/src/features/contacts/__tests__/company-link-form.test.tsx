import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { CompanyLinkForm } from '../company-link-form'
import type { CrmCompany } from '@/features/crm/api'

// This form is the only thing in the product that writes `contacts.company_id`.
// Its risks are all in the request body: an omitted `company_id` is a 400, and an
// unlink has to be an explicit null rather than an absent field — "absent" must
// never quietly mean "detach".

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
  domain: `${name.toLowerCase()}.test`,
  currency: 'USD',
  deal_count: 0,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
})

let companiesResponse: () => Response
let linkResponse: () => Response
let puts: { url: string; body: unknown }[]

beforeEach(() => {
  companiesResponse = () => json({ items: [company('co-1', 'Acme'), company('co-2', 'Globex')] })
  linkResponse = () => json({ id: 'c-1' })
  puts = []

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : new Request(input)
      const url = new URL(request.url)
      if (url.pathname.endsWith('/contacts/c-1/company')) {
        puts.push({ url: url.pathname, body: JSON.parse(await request.text()) as unknown })
        return linkResponse()
      }
      if (url.pathname.endsWith('/crm/companies')) return companiesResponse()
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
  return screen.findByLabelText('Company')
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
  const select = await openPicker(/link this contact/i)
  await waitFor(() => expect(screen.getByRole('option', { name: 'Globex' })).toBeInTheDocument())

  fireEvent.change(select, { target: { value: 'co-2' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  await waitFor(() => expect(puts).toHaveLength(1))
  expect(puts[0]).toEqual({ url: '/api/v1/contacts/c-1/company', body: { company_id: 'co-2' } })
})

test('unlinking sends an explicit null, never an omitted field', async () => {
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={linked} />)
  const select = await openPicker(/change the company/i)

  fireEvent.change(select, { target: { value: '' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  await waitFor(() => expect(puts).toHaveLength(1))
  // The API rejects `{}` with a 400 on purpose, so that an absent field can never
  // be read as "detach". The body must carry the null.
  expect(puts[0]?.body).toEqual({ company_id: null })
  expect(Object.keys(puts[0]?.body as object)).toContain('company_id')
})

test('the currently linked company stays selectable even if it is past the page cap', async () => {
  // The picker asks for one capped page of companies. If the linked one is not on
  // it, opening the form would otherwise silently propose unlinking.
  companiesResponse = () => json({ items: [company('co-9', 'Someone else')], next_cursor: 'more' })
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={linked} />)
  const select = await openPicker(/change the company/i)

  await waitFor(() => expect(screen.getByRole('option', { name: 'Someone else' })).toBeInTheDocument())
  expect(screen.getByRole('option', { name: 'Acme' })).toBeInTheDocument()
  expect(select).toHaveValue('co-1')
})

test('a missing company and a missing contact read differently, because the fix differs', async () => {
  linkResponse = () => json({ error: 'company not found' }, 404)
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  const select = await openPicker(/link this contact/i)
  await waitFor(() => expect(screen.getByRole('option', { name: 'Acme' })).toBeInTheDocument())
  fireEvent.change(select, { target: { value: 'co-1' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/that company no longer exists/i)

  linkResponse = () => json({ error: 'contact not found' }, 404)
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  expect(await screen.findByText(/this contact no longer exists/i)).toBeInTheDocument()
})

test('the form stays open on failure so the choice is not lost', async () => {
  linkResponse = () => json({ error: 'boom' }, 500)
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  const select = await openPicker(/link this contact/i)
  await waitFor(() => expect(screen.getByRole('option', { name: 'Acme' })).toBeInTheDocument())
  fireEvent.change(select, { target: { value: 'co-1' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('The server had a problem.')
  expect(screen.getByLabelText('Company')).toHaveValue('co-1')
})

test('a failed company list explains why there is nothing to choose from', async () => {
  companiesResponse = () => json({ error: 'boom' }, 500)
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={null} />)
  await openPicker(/link this contact/i)

  expect(await screen.findByRole('alert')).toHaveTextContent(/company list could not be loaded/i)
})

test('cancelling closes the form without writing', async () => {
  renderWithProviders(<CompanyLinkForm contactId="c-1" company={linked} />)
  await openPicker(/change the company/i)

  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

  expect(await screen.findByRole('link', { name: 'Acme' })).toBeInTheDocument()
  expect(puts).toHaveLength(0)
})

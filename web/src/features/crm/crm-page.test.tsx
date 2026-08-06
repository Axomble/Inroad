import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { CRMPage } from './crm-page'
import type { CrmCompany, CrmDeal, CrmPipeline } from './api'

// The CRM console's risky parts are all invisible when wrong: an amount is
// stored a thousand times too large, a deal is filed under another pipeline's
// stage, or a permissions failure is reported as "check your connection".
// These tests drive the real fetch round-trip and read the bodies that leave.

const jsonHeaders = { 'content-type': 'application/json' }

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: jsonHeaders })
}

const stage = (id: string, label: string, extra: Partial<CrmPipeline['stages'][number]> = {}) => ({
  id,
  pipeline_id: 'p-1',
  key: label.toLowerCase(),
  label,
  color: '#888888',
  position: 1,
  is_won: false,
  is_lost: false,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...extra,
})

const pipeline = (id: string, name: string, stages: CrmPipeline['stages']): CrmPipeline => ({
  id,
  name,
  is_default: id === 'p-1',
  stages,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
})

const deal = (overrides: Partial<CrmDeal> = {}): CrmDeal => ({
  id: 'd-1',
  pipeline_id: 'p-1',
  stage_id: 's-1',
  name: 'Acme renewal',
  currency: 'USD',
  amount_micros: 1_000_000_000,
  // Server-computed fractional board ordering; the UI never writes it.
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

const uuidPipeline = '11111111-1111-4111-8111-111111111111'
const uuidOther = '22222222-2222-4222-8222-222222222222'
const uuidStageA = '33333333-3333-4333-8333-333333333333'
const uuidStageB = '44444444-4444-4444-8444-444444444444'

let companiesResponse: () => Response
let pipelinesResponse: () => Response
let dealsResponse: () => Response
let createDealResponse: () => Response
let createCompanyResponse: () => Response
let dealPosts: Record<string, unknown>[]
let companyPosts: Record<string, unknown>[]
let dealListRequests: number
/** Every GET that went out, so the pagination args can be inspected. */
let listRequests: URL[]

beforeEach(() => {
  companiesResponse = () => json({ items: [company()] })
  pipelinesResponse = () =>
    json({ items: [pipeline('p-1', 'Sales', [stage('s-1', 'Qualified'), stage('s-2', 'Won', { is_won: true })])] })
  dealsResponse = () => json({ items: [deal()] })
  createDealResponse = () => json(deal({ id: 'd-new' }), 201)
  createCompanyResponse = () => json(company({ id: 'co-new' }), 201)
  dealPosts = []
  companyPosts = []
  dealListRequests = 0
  listRequests = []

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : new Request(input)
      const url = new URL(request.url)
      const { pathname } = url
      if (request.method === 'GET') listRequests.push(url)

      if (pathname.endsWith('/crm/settings')) return json({ auto_capture_policy: 'sent', updated_at: '2026-08-01T00:00:00Z' })
      if (pathname.endsWith('/crm/companies')) {
        if (request.method === 'POST') {
          companyPosts.push(JSON.parse(await request.text()) as Record<string, unknown>)
          return createCompanyResponse()
        }
        return companiesResponse()
      }
      if (pathname.endsWith('/crm/pipelines')) return pipelinesResponse()
      if (pathname.endsWith('/crm/deals')) {
        if (request.method === 'POST') {
          dealPosts.push(JSON.parse(await request.text()) as Record<string, unknown>)
          return createDealResponse()
        }
        dealListRequests += 1
        return dealsResponse()
      }
      throw new Error(`unexpected request: ${request.method} ${request.url}`)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** Renders the page and waits for the default (Deals) tab to settle. */
async function renderPage() {
  const view = renderWithProviders(<CRMPage />)
  await waitFor(() => expect(screen.queryByLabelText('Loading CRM')).not.toBeInTheDocument())
  return view
}

async function openDealForm() {
  fireEvent.click(screen.getByRole('button', { name: /new deal/i }))
  await screen.findByLabelText('Deal name')
}

function statValue(label: string): string {
  // "Companies" also names a tab, so the lookup is scoped to the stat strip.
  const stat = screen.getAllByText(label).map((node) => node.closest('[data-slot="stat"]')).find(Boolean)
  if (!stat) throw new Error(`no stat named ${label}`)
  return stat.textContent ?? ''
}

test('converts a whole-unit amount to micros and anchors the close date at UTC midnight', async () => {
  pipelinesResponse = () =>
    json({ items: [pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')])] })
  await renderPage()
  await openDealForm()

  fireEvent.change(screen.getByLabelText('Deal name'), { target: { value: 'Big deal' } })
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidPipeline } })
  fireEvent.change(screen.getByLabelText('Stage'), { target: { value: uuidStageA } })
  fireEvent.change(screen.getByLabelText('Amount'), { target: { value: '1234.56' } })
  fireEvent.change(screen.getByLabelText('Currency'), { target: { value: 'usd' } })
  fireEvent.change(screen.getByLabelText('Expected close'), { target: { value: '2026-09-01' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  await waitFor(() => expect(dealPosts).toHaveLength(1))
  expect(dealPosts[0]).toEqual({
    name: 'Big deal',
    pipeline_id: uuidPipeline,
    stage_id: uuidStageA,
    amount_micros: 1_234_560_000,
    currency: 'USD',
    close_date: '2026-09-01T00:00:00Z',
  })
})

test('omits optional fields left blank rather than sending zero or an empty string', async () => {
  pipelinesResponse = () =>
    json({ items: [pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')])] })
  await renderPage()
  await openDealForm()

  fireEvent.change(screen.getByLabelText('Deal name'), { target: { value: 'Minimal' } })
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidPipeline } })
  fireEvent.change(screen.getByLabelText('Stage'), { target: { value: uuidStageA } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  await waitFor(() => expect(dealPosts).toHaveLength(1))
  const body = dealPosts[0] as Record<string, unknown>
  expect(body).not.toHaveProperty('amount_micros')
  expect(body).not.toHaveProperty('close_date')
  expect(body).not.toHaveProperty('company_id')
})

test('company revenue converts to micros and an unset revenue is omitted', async () => {
  await renderPage()
  fireEvent.click(screen.getByRole('tab', { name: 'Companies' }))
  fireEvent.click(screen.getByRole('button', { name: /new company/i }))
  await screen.findByLabelText('Company name')

  fireEvent.change(screen.getByLabelText('Company name'), { target: { value: 'Globex' } })
  fireEvent.change(screen.getByLabelText('Domain'), { target: { value: 'globex.test' } })
  fireEvent.change(screen.getByLabelText('Annual revenue'), { target: { value: '2.5' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  await waitFor(() => expect(companyPosts).toHaveLength(1))
  expect(companyPosts[0]).toEqual({ name: 'Globex', domain: 'globex.test', currency: 'USD', annual_revenue_micros: 2_500_000 })
})

test('changing the pipeline clears the stage so a foreign stage can never be submitted', async () => {
  pipelinesResponse = () =>
    json({
      items: [
        pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')]),
        pipeline(uuidOther, 'Partners', [stage(uuidStageB, 'Intro')]),
      ],
    })
  await renderPage()
  await openDealForm()

  // Stage is unreachable until a pipeline is chosen.
  expect(screen.getByLabelText('Stage')).toBeDisabled()

  fireEvent.change(screen.getByLabelText('Deal name'), { target: { value: 'Cross pipeline' } })
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidPipeline } })
  fireEvent.change(screen.getByLabelText('Stage'), { target: { value: uuidStageA } })
  // Switching pipelines drops the previously chosen stage.
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidOther } })
  expect(screen.getByLabelText('Stage')).toHaveValue('')

  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByText('Select a stage')).toBeInTheDocument()
  expect(dealPosts).toHaveLength(0)
})

test('rejects a currency that is not three letters before it reaches the server', async () => {
  pipelinesResponse = () => json({ items: [pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')])] })
  await renderPage()
  await openDealForm()

  fireEvent.change(screen.getByLabelText('Deal name'), { target: { value: 'Bad currency' } })
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidPipeline } })
  fireEvent.change(screen.getByLabelText('Stage'), { target: { value: uuidStageA } })
  fireEvent.change(screen.getByLabelText('Currency'), { target: { value: '12a' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  expect(await screen.findByText('Use a three-letter currency code')).toBeInTheDocument()
  expect(dealPosts).toHaveLength(0)
})

test("surfaces the server's own reason when a create is rejected", async () => {
  pipelinesResponse = () => json({ items: [pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')])] })
  createDealResponse = () => json({ error: 'currency must be a three-letter ISO code' }, 422)
  await renderPage()
  await openDealForm()

  fireEvent.change(screen.getByLabelText('Deal name'), { target: { value: 'Rejected' } })
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidPipeline } })
  fireEvent.change(screen.getByLabelText('Stage'), { target: { value: uuidStageA } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  expect(await screen.findByText('currency must be a three-letter ISO code')).toBeInTheDocument()
  // The form stays open on failure so the entered values survive.
  expect(screen.getByLabelText('Deal name')).toHaveValue('Rejected')
})

test('a successful create closes the form and refetches the list through the generated endpoint tags', async () => {
  pipelinesResponse = () => json({ items: [pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')])] })
  await renderPage()
  const listRequestsBefore = dealListRequests
  await openDealForm()

  fireEvent.change(screen.getByLabelText('Deal name'), { target: { value: 'Fresh' } })
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidPipeline } })
  fireEvent.change(screen.getByLabelText('Stage'), { target: { value: uuidStageA } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  await waitFor(() => expect(screen.queryByLabelText('Deal name')).not.toBeInTheDocument())
  await waitFor(() => expect(dealListRequests).toBeGreaterThan(listRequestsBefore))
})

test('a failing companies request leaves the deals tab usable', async () => {
  companiesResponse = () => json({ error: 'boom' }, 500)
  await renderPage()

  expect(screen.getByText('Acme renewal')).toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()

  fireEvent.click(screen.getByRole('tab', { name: 'Companies' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('The server had a problem loading CRM data.')
})

test('a missing CRM scope reads as a permission problem, and retry re-requests the list', async () => {
  dealsResponse = () => json({ error: 'forbidden' }, 403)
  renderWithProviders(<CRMPage />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('crm:read')
  const requestsBefore = dealListRequests

  dealsResponse = () => json({ items: [deal()] })
  fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))

  expect(await screen.findByText('Acme renewal')).toBeInTheDocument()
  expect(dealListRequests).toBeGreaterThan(requestsBefore)
})

test('the deal form is gated on pipelines, not the deal list', async () => {
  pipelinesResponse = () => json({ error: 'boom' }, 500)
  await renderPage()

  expect(screen.getByText('Acme renewal')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: /new deal/i }))
  expect(await screen.findByRole('alert')).toHaveTextContent('The server had a problem loading CRM data.')
  expect(screen.queryByLabelText('Deal name')).not.toBeInTheDocument()
})

test('empty lists explain what to do instead of rendering a blank tab', async () => {
  dealsResponse = () => json({ items: [] })
  companiesResponse = () => json({ items: [] })
  pipelinesResponse = () => json({ items: [] })
  renderWithProviders(<CRMPage />)

  expect(await screen.findByText('No deals in the pipeline')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('tab', { name: 'Companies' }))
  expect(await screen.findByText('No companies yet')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('tab', { name: 'Pipelines' }))
  expect(await screen.findByText('No pipelines')).toBeInTheDocument()
})

test('open pipeline counts only deals still in play', async () => {
  dealsResponse = () =>
    json({
      items: [
        deal({ id: 'd-open', amount_micros: 1_000_000_000 }),
        deal({ id: 'd-won', amount_micros: 5_000_000_000, stage_is_won: true }),
        deal({ id: 'd-lost', amount_micros: 9_000_000_000, stage_is_lost: true }),
      ],
    })
  await renderPage()

  expect(statValue('Open pipeline')).toContain(money(1000))
  expect(statValue('Open pipeline')).toContain('1 open deal')
  expect(statValue('Won')).toContain('1')
})

test('a mixed-currency pipeline refuses to invent a single total', async () => {
  dealsResponse = () =>
    json({
      items: [
        deal({ id: 'd-usd', amount_micros: 1_000_000_000 }),
        deal({ id: 'd-eur', amount_micros: 2_000_000_000, currency: 'EUR' }),
      ],
    })
  await renderPage()

  expect(statValue('Open pipeline')).toContain('—')
  expect(statValue('Open pipeline')).toContain('2 currencies')
})

test('a zero revenue is shown as zero, not as "not set"', async () => {
  companiesResponse = () => json({ items: [company({ annual_revenue_micros: 0 })] })
  await renderPage()
  fireEvent.click(screen.getByRole('tab', { name: 'Companies' }))

  expect(await screen.findByText(money(0))).toBeInTheDocument()
  expect(screen.queryByText('Revenue not set')).not.toBeInTheDocument()
})

test('asks for a full page and says so when the server held records back', async () => {
  dealsResponse = () => json({ items: [deal()], next_cursor: 'opaque-cursor' })
  companiesResponse = () => json({ items: [company()], next_cursor: 'opaque-cursor' })
  await renderPage()

  // The request carries an explicit limit rather than taking the default 50.
  expect(listRequests.find((url) => url.pathname.endsWith('/crm/deals'))?.searchParams.get('limit')).toBe('200')

  expect(screen.getByRole('status')).toHaveTextContent('Showing the first 1 deals')
  // A partial list must not present its totals as the whole workspace.
  expect(statValue('Open pipeline')).toContain('loaded so far')

  fireEvent.click(screen.getByRole('tab', { name: 'Companies' }))
  expect(await screen.findByText(/Showing the first 1 companies/)).toBeInTheDocument()
  expect(statValue('Companies')).toContain('1+')
})

test('a complete list says nothing about paging', async () => {
  await renderPage()

  expect(screen.queryByRole('status')).not.toBeInTheDocument()
  expect(statValue('Companies')).not.toContain('+')
  expect(statValue('Open pipeline')).not.toContain('loaded so far')
})

test('the tabs implement the keyboard pattern they advertise', async () => {
  await renderPage()
  const dealsTab = screen.getByRole('tab', { name: 'Deals' })
  const companiesTab = screen.getByRole('tab', { name: 'Companies' })

  // Roving tabindex: exactly one stop for the whole set.
  expect(dealsTab).toHaveAttribute('tabindex', '0')
  expect(companiesTab).toHaveAttribute('tabindex', '-1')

  fireEvent.keyDown(dealsTab, { key: 'ArrowRight' })
  await waitFor(() => expect(companiesTab).toHaveAttribute('aria-selected', 'true'))
  expect(companiesTab).toHaveFocus()

  // The panel is real and is named by the selected tab.
  const panel = screen.getByRole('tabpanel')
  expect(panel).toHaveAttribute('aria-labelledby', companiesTab.id)
  expect(companiesTab).toHaveAttribute('aria-controls', panel.id)

  fireEvent.keyDown(companiesTab, { key: 'End' })
  await waitFor(() => expect(screen.getByRole('tab', { name: 'Pipelines' })).toHaveAttribute('aria-selected', 'true'))
})

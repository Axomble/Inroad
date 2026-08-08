import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { DealsPage } from './deals-page'
import type { CrmBoard, CrmPipeline } from './api'

// Deals is now the single deals surface: the board, the pipelines that define its
// stages, and the form that creates one. The board's whole risk sits in the
// optimistic move — the card has to jump columns before the server answers and
// snap back, with a reason, when it refuses — which only works if the patch and
// the rendered board are the same cache entry. The form's risk is silent: an
// amount stored a thousand times too large, or a stage from another pipeline.

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

const uuidPipeline = '11111111-1111-4111-8111-111111111111'
const uuidOther = '22222222-2222-4222-8222-222222222222'
const uuidStageA = '33333333-3333-4333-8333-333333333333'
const uuidStageB = '44444444-4444-4444-8444-444444444444'

const stage = (id: string, label: string) => ({
  id,
  pipeline_id: 'p-1',
  key: label.toLowerCase(),
  label,
  color: '#888888',
  position: 1,
  is_won: label === 'Won',
  is_lost: false,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
})

const pipeline = (id: string, name: string, stages: CrmPipeline['stages']): CrmPipeline => ({
  id,
  name,
  is_default: id === 'p-1',
  stages,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
})

// Typed against the generated contract so a schema change breaks here loudly
// rather than producing a board that only looks right.
const boardBody = (): CrmBoard => ({
  pipeline: pipeline('p-1', 'Sales', [stage('s-1', 'Qualified'), stage('s-2', 'Won')]),
  stages: [
    {
      stage: stage('s-1', 'Qualified'),
      deals: [
        {
          id: 'd-1',
          pipeline_id: 'p-1',
          stage_id: 's-1',
          company_id: 'co-1',
          company_name: 'Acme',
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
        },
      ],
      deal_count: 1,
      amount_micros: 1_000_000_000,
    },
    { stage: stage('s-2', 'Won'), deals: [], deal_count: 0, amount_micros: 0 },
  ],
})

let board: CrmBoard
let boardResponse: () => Response
let pipelinesResponse: () => Response
let createDealResponse: () => Response
let moveResponse: () => Promise<Response> | Response
let movePosts: { url: string; body: Record<string, unknown> }[]
let dealPosts: Record<string, unknown>[]

beforeEach(() => {
  board = boardBody()
  boardResponse = () => json(board)
  pipelinesResponse = () => json({ items: [pipeline('p-1', 'Sales', [stage('s-1', 'Qualified')])] })
  createDealResponse = () => json({ ...board.stages[0]?.deals[0], id: 'd-new' }, 201)
  moveResponse = () => json({ ...board.stages[0]?.deals[0], stage_id: 's-2' })
  movePosts = []
  dealPosts = []

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : new Request(input)
      const url = new URL(request.url)
      const { pathname } = url
      if (pathname.endsWith('/move')) {
        movePosts.push({ url: pathname, body: JSON.parse(await request.text()) as Record<string, unknown> })
        return moveResponse()
      }
      if (pathname.endsWith('/crm/board')) return boardResponse()
      if (pathname.endsWith('/crm/pipelines')) return pipelinesResponse()
      if (pathname.endsWith('/crm/companies')) return json({ items: [] })
      if (pathname.endsWith('/crm/settings')) return json({ auto_capture_policy: 'sent', updated_at: '2026-08-01T00:00:00Z' })
      if (pathname.endsWith('/crm/deals')) {
        dealPosts.push(JSON.parse(await request.text()) as Record<string, unknown>)
        return createDealResponse()
      }
      throw new Error(`unexpected request: ${request.method} ${request.url}`)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function column(label: string): HTMLElement {
  const heading = screen.getByRole('heading', { name: label })
  const section = heading.closest('section')
  if (!section) throw new Error(`no column for ${label}`)
  return section
}

async function openDealForm() {
  fireEvent.click(screen.getByRole('button', { name: /new deal/i }))
  await screen.findByLabelText('Deal name')
}

test('moves a deal in the rendered board before the server answers, and reports the move', async () => {
  let release: (() => void) | undefined
  moveResponse = async () => {
    await new Promise<void>((resolve) => { release = resolve })
    return json({ id: 'd-1', stage_id: 's-2' })
  }
  renderWithProviders(<DealsPage />)
  await screen.findByText('Acme renewal')

  fireEvent.change(within(column('Qualified')).getByLabelText('Move to stage'), { target: { value: 's-2' } })

  // The patch lands on the same cache entry the board reads from.
  await waitFor(() => expect(within(column('Won')).getByText('Acme renewal')).toBeInTheDocument())
  expect(within(column('Qualified')).queryByText('Acme renewal')).not.toBeInTheDocument()
  expect(movePosts).toEqual([{ url: '/api/v1/crm/deals/d-1/move', body: { stage_id: 's-2' } }])

  release?.()
  expect(await screen.findByText('Acme renewal moved to Won.')).toBeInTheDocument()
})

test('rolls the board back and says why when the server refuses the move', async () => {
  moveResponse = () => json({ error: 'stage belongs to another pipeline' }, 409)
  renderWithProviders(<DealsPage />)
  await screen.findByText('Acme renewal')

  fireEvent.change(within(column('Qualified')).getByLabelText('Move to stage'), { target: { value: 's-2' } })

  expect(await screen.findByText(/stage belongs to another pipeline/)).toBeInTheDocument()
  expect(within(column('Qualified')).getByText('Acme renewal')).toBeInTheDocument()
  expect(within(column('Won')).queryByText('Acme renewal')).not.toBeInTheDocument()
})

test('an agent-created deal is tellable from a hand-made one on the board', async () => {
  const qualified = board.stages[0]
  const manual = qualified?.deals[0]
  if (!qualified || !manual) throw new Error('fixture lost its deal')
  qualified.deals = [
    manual,
    { ...manual, id: 'd-2', name: 'Agent-sourced lead', source: 'agent', created_by_actor: { type: 'agent', client_id: 'cli-9' } },
  ]
  qualified.deal_count = 2
  renderWithProviders(<DealsPage />)

  const agentCard = (await screen.findByText('Agent-sourced lead')).closest('article')
  const manualCard = screen.getByText('Acme renewal').closest('article')
  if (!agentCard || !manualCard) throw new Error('deal cards did not render')
  expect(within(agentCard).getByText('Agent / cli-9')).toBeInTheDocument()
  expect(within(agentCard).getByTitle(/Created by an AI agent/)).toBeInTheDocument()
  expect(within(manualCard).getByText('Workspace member')).toBeInTheDocument()
})

test('a card reaches the account as well as the opportunity', async () => {
  renderWithProviders(<DealsPage />)

  expect(await screen.findByRole('link', { name: 'Acme renewal' })).toHaveAttribute('href', '/app/deals/d-1')
  expect(screen.getByRole('link', { name: 'Acme' })).toHaveAttribute('href', '/app/companies/co-1')
})

test('a failed board load explains the failure and offers a retry', async () => {
  boardResponse = () => json({ error: 'boom' }, 500)
  renderWithProviders(<DealsPage />)

  expect(await screen.findByText('The pipeline could not be loaded')).toBeInTheDocument()
  expect(screen.getByText('The server had a problem loading CRM data. Try again in a moment.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
})

test('an empty board offers the create form rather than sending the reader elsewhere', async () => {
  for (const empty of board.stages) {
    empty.deals = []
    empty.deal_count = 0
    empty.amount_micros = 0
  }
  renderWithProviders(<DealsPage />)

  fireEvent.click(await screen.findByRole('button', { name: 'New deal' }))
  expect(await screen.findByLabelText('Deal name')).toBeInTheDocument()
})

test('converts a whole-unit amount to micros and anchors the close date at UTC midnight', async () => {
  pipelinesResponse = () => json({ items: [pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')])] })
  renderWithProviders(<DealsPage />)
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
  pipelinesResponse = () => json({ items: [pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')])] })
  renderWithProviders(<DealsPage />)
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

test('changing the pipeline clears the stage so a foreign stage can never be submitted', async () => {
  pipelinesResponse = () =>
    json({
      items: [
        pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')]),
        pipeline(uuidOther, 'Partners', [stage(uuidStageB, 'Intro')]),
      ],
    })
  renderWithProviders(<DealsPage />)
  await openDealForm()

  // Stage is unreachable until a pipeline is chosen.
  expect(screen.getByLabelText('Stage')).toBeDisabled()

  fireEvent.change(screen.getByLabelText('Deal name'), { target: { value: 'Cross pipeline' } })
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidPipeline } })
  fireEvent.change(screen.getByLabelText('Stage'), { target: { value: uuidStageA } })
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidOther } })
  expect(screen.getByLabelText('Stage')).toHaveValue('')

  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByText('Select a stage')).toBeInTheDocument()
  expect(dealPosts).toHaveLength(0)
})

test("surfaces the server's own reason when a create is rejected, keeping the values entered", async () => {
  pipelinesResponse = () => json({ items: [pipeline(uuidPipeline, 'Sales', [stage(uuidStageA, 'Qualified')])] })
  createDealResponse = () => json({ error: 'currency must be a three-letter ISO code' }, 422)
  renderWithProviders(<DealsPage />)
  await openDealForm()

  fireEvent.change(screen.getByLabelText('Deal name'), { target: { value: 'Rejected' } })
  fireEvent.change(screen.getByLabelText('Pipeline'), { target: { value: uuidPipeline } })
  fireEvent.change(screen.getByLabelText('Stage'), { target: { value: uuidStageA } })
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))

  expect(await screen.findByText('currency must be a three-letter ISO code')).toBeInTheDocument()
  expect(screen.getByLabelText('Deal name')).toHaveValue('Rejected')
})

test('the deal form is gated on pipelines, not on the board', async () => {
  pipelinesResponse = () => json({ error: 'boom' }, 500)
  renderWithProviders(<DealsPage />)

  expect(await screen.findByText('Acme renewal')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: /new deal/i }))
  expect(await screen.findByRole('alert')).toHaveTextContent('The server had a problem loading CRM data.')
  expect(screen.queryByLabelText('Deal name')).not.toBeInTheDocument()
})

test('the Pipelines tab shows the stages, and its create button changes with the tab', async () => {
  renderWithProviders(<DealsPage />)
  await screen.findByText('Acme renewal')

  fireEvent.click(screen.getByRole('tab', { name: 'Pipelines' }))

  expect(await screen.findByRole('heading', { name: 'Sales' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /new pipeline/i })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /new deal/i })).not.toBeInTheDocument()
})

test('an empty pipelines list explains what to do instead of rendering a blank tab', async () => {
  pipelinesResponse = () => json({ items: [] })
  renderWithProviders(<DealsPage />)
  fireEvent.click(await screen.findByRole('tab', { name: 'Pipelines' }))

  expect(await screen.findByText('No pipelines')).toBeInTheDocument()
})

test('the tabs implement the keyboard pattern they advertise', async () => {
  renderWithProviders(<DealsPage />)
  const boardTab = await screen.findByRole('tab', { name: 'Board' })
  const pipelinesTab = screen.getByRole('tab', { name: 'Pipelines' })

  // Roving tabindex: exactly one stop for the whole set.
  expect(boardTab).toHaveAttribute('tabindex', '0')
  expect(pipelinesTab).toHaveAttribute('tabindex', '-1')

  fireEvent.keyDown(boardTab, { key: 'ArrowRight' })
  await waitFor(() => expect(pipelinesTab).toHaveAttribute('aria-selected', 'true'))
  expect(pipelinesTab).toHaveFocus()

  // The panel is real and is named by the selected tab.
  const panel = screen.getByRole('tabpanel')
  expect(panel).toHaveAttribute('aria-labelledby', pipelinesTab.id)
  expect(pipelinesTab).toHaveAttribute('aria-controls', panel.id)

  fireEvent.keyDown(pipelinesTab, { key: 'Home' })
  await waitFor(() => expect(boardTab).toHaveAttribute('aria-selected', 'true'))
})

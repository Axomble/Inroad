import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { DealsBoardPage } from './deals-board-page'
import type { CrmBoard } from './api'

// The board's whole risk sits in the optimistic move: the card has to jump
// columns before the server answers, and snap back — with a reason — when it
// refuses. That only works if the patch and the rendered board are the same
// cache entry, which is exactly what a second, hand-injected `/crm/board`
// endpoint would quietly break.

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, ...props }: { children: React.ReactNode; to?: string; params?: unknown }) => {
    const { to, params, ...rest } = props as Record<string, unknown>
    void params
    return <a href={typeof to === 'string' ? to : '#'} {...rest}>{children}</a>
  },
}))

const jsonHeaders = { 'content-type': 'application/json' }

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: jsonHeaders })
}

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

// Typed against the generated contract so a schema change breaks here loudly
// rather than producing a board that only looks right.
const boardBody = (): CrmBoard => ({
  pipeline: {
    id: 'p-1',
    name: 'Sales',
    is_default: true,
    stages: [stage('s-1', 'Qualified'), stage('s-2', 'Won')],
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
  stages: [
    {
      stage: stage('s-1', 'Qualified'),
      deals: [
        {
          id: 'd-1',
          pipeline_id: 'p-1',
          stage_id: 's-1',
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

let movePosts: { url: string; body: Record<string, unknown> }[]
let moveResponse: () => Promise<Response> | Response

beforeEach(() => {
  movePosts = []
  moveResponse = () => json({ ...boardBody().stages[0]?.deals[0], stage_id: 's-2' })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : new Request(input)
      const url = new URL(request.url)
      if (url.pathname.endsWith('/move')) {
        movePosts.push({ url: url.pathname, body: JSON.parse(await request.text()) as Record<string, unknown> })
        return moveResponse()
      }
      if (url.pathname.endsWith('/crm/board')) return json(boardBody())
      if (url.pathname.endsWith('/crm/deals')) return json({ items: [] })
      if (url.pathname.endsWith('/crm/events')) return json({ items: [] })
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

test('moves a deal in the rendered board before the server answers, and reports the move', async () => {
  let release: (() => void) | undefined
  moveResponse = async () => {
    await new Promise<void>((resolve) => { release = resolve })
    return json({ id: 'd-1', stage_id: 's-2' })
  }
  renderWithProviders(<DealsBoardPage />)
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
  renderWithProviders(<DealsBoardPage />)
  await screen.findByText('Acme renewal')

  fireEvent.change(within(column('Qualified')).getByLabelText('Move to stage'), { target: { value: 's-2' } })

  expect(await screen.findByText(/stage belongs to another pipeline/)).toBeInTheDocument()
  expect(within(column('Qualified')).getByText('Acme renewal')).toBeInTheDocument()
  expect(within(column('Won')).queryByText('Acme renewal')).not.toBeInTheDocument()
})

test('an agent-created deal is tellable from a hand-made one on the board', async () => {
  const board = boardBody()
  const qualified = board.stages[0]
  const manual = qualified?.deals[0]
  if (!qualified || !manual) throw new Error('fixture lost its deal')
  qualified.deals = [
    manual,
    { ...manual, id: 'd-2', name: 'Agent-sourced lead', source: 'agent', created_by_actor: { type: 'agent', client_id: 'cli-9' } },
  ]
  qualified.deal_count = 2
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    const url = new URL(input instanceof Request ? input.url : String(input))
    if (url.pathname.endsWith('/crm/board')) return Promise.resolve(json(board))
    throw new Error(`unexpected request: ${url.pathname}`)
  }))
  renderWithProviders(<DealsBoardPage />)

  const agentCard = (await screen.findByText('Agent-sourced lead')).closest('article')
  const manualCard = screen.getByText('Acme renewal').closest('article')
  if (!agentCard || !manualCard) throw new Error('deal cards did not render')
  expect(within(agentCard).getByText('Agent / cli-9')).toBeInTheDocument()
  expect(within(agentCard).getByTitle(/Created by an AI agent/)).toBeInTheDocument()
  expect(within(manualCard).getByText('Workspace member')).toBeInTheDocument()
})

test('a failed board load explains the failure and offers a retry', async () => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(json({ error: 'boom' }, 500))))
  renderWithProviders(<DealsBoardPage />)

  expect(await screen.findByText('The pipeline could not be loaded')).toBeInTheDocument()
  expect(screen.getByText('The server had a problem loading CRM data. Try again in a moment.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
})

import { screen, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { DealDetailPage } from '../deal-detail-page'
import type { CrmDeal } from '../api'
import type { CrmNote, CrmTask } from '@/features/records/api'

// The detail page is where a reader decides whether to trust a record. Every
// row it shows — the deal, its notes, its tasks, its events — has to say who
// made it, and an agent-made one has to be tellable from a human-made one.

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, ...props }: { children: React.ReactNode; to?: string; params?: unknown }) => {
    const { to, params, ...rest } = props as Record<string, unknown>
    void params
    return <a href={typeof to === 'string' ? to : '#'} {...rest}>{children}</a>
  },
}))

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

const deal = (): CrmDeal => ({
  id: 'd-1',
  pipeline_id: 'p-1',
  stage_id: 's-1',
  name: 'Acme renewal',
  currency: 'USD',
  amount_micros: 1_000_000_000,
  position: 1,
  source: 'reply',
  created_by_actor: { type: 'system' },
  pipeline_name: 'Sales',
  stage_label: 'Qualified',
  stage_color: '#888888',
  stage_is_won: false,
  stage_is_lost: false,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
})

const note = (): CrmNote => ({
  id: 'n-1',
  title: 'Discovery summary',
  body: 'They renew in Q4.',
  created_by_actor: { type: 'agent', client_id: 'cli-9' },
  created_at: '2026-08-02T00:00:00Z',
  updated_at: '2026-08-02T00:00:00Z',
})

const task = (): CrmTask => ({
  id: 't-1',
  title: 'Book a discovery call',
  body: '',
  status: 'open',
  created_by_actor: { type: 'user' },
  created_at: '2026-08-02T00:00:00Z',
  updated_at: '2026-08-02T00:00:00Z',
})

const agentEvent = {
  id: 'e-1',
  name: 'deal.created',
  actor: { type: 'agent', client_id: 'cli-9', thread_id: 'th-1', run_id: 'run-2' },
  data: {},
  occurred_at: '2026-08-02T00:00:00Z',
}

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = new URL(input instanceof Request ? input.url : String(input))
      if (url.pathname.endsWith('/crm/events')) return Promise.resolve(json({ items: [agentEvent] }))
      if (url.pathname.endsWith('/crm/notes')) return Promise.resolve(json({ items: [note()] }))
      if (url.pathname.endsWith('/crm/tasks')) return Promise.resolve(json({ items: [task()] }))
      if (url.pathname.endsWith('/threads')) return Promise.resolve(json({ items: [] }))
      if (url.pathname.endsWith('/crm/deals/d-1')) return Promise.resolve(json(deal()))
      throw new Error(`unexpected request: ${url.pathname}`)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

test('attributes the deal itself, not only its events', async () => {
  renderWithProviders(<DealDetailPage dealId="d-1" />)

  // source=reply with a system actor: auto-captured, not a person's doing.
  const badge = await screen.findByText('Auto-captured')
  expect(badge.title).toContain('positive campaign reply')
})

test('attributes notes and tasks to whoever created them', async () => {
  renderWithProviders(<DealDetailPage dealId="d-1" />)

  const noteCard = (await screen.findByText('They renew in Q4.')).closest('article')
  if (!noteCard) throw new Error('the note did not render')
  expect(within(noteCard).getByText('Agent / cli-9')).toBeInTheDocument()

  const taskItem = screen.getByText('Book a discovery call').closest('li')
  if (!taskItem) throw new Error('the task did not render')
  expect(within(taskItem).getByText('Workspace member')).toBeInTheDocument()
})

test('the activity feed still names the agent and its run after the extraction', async () => {
  renderWithProviders(<DealDetailPage dealId="d-1" />)

  const row = (await screen.findByText('deal created')).closest('li')
  if (!row) throw new Error('the event did not render')
  expect(within(row).getByText('Agent / cli-9')).toBeInTheDocument()
  expect(within(row).getByText('Agent thread th-1 / run run-2')).toBeInTheDocument()
})

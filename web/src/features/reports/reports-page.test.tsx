import { screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { CampaignReport } from '@/store/api'
import { ReportsPage } from './reports-page'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, params, children, ...props }: { to: string; params?: { id?: string }; children: React.ReactNode }) => (
    <a href={params?.id ? to.replace('$id', params.id) : to} {...props}>
      {children}
    </a>
  ),
}))

const jsonHeaders = { 'content-type': 'application/json' }

function counts(overrides: Partial<CampaignReport['totals']> = {}): CampaignReport['totals'] {
  return {
    sent: 0, enrolled: 0, opens: 0, clicks: 0, replies: 0, bounces: 0, unsubscribes: 0,
    open_rate: 0, click_rate: 0, reply_rate: 0, bounce_rate: 0, unsub_rate: 0,
    ...overrides,
  }
}

let respond: () => Response

beforeEach(() => {
  respond = () =>
    new Response(JSON.stringify({ campaigns: [], totals: counts() }), { status: 200, headers: jsonHeaders })
  vi.stubGlobal('fetch', vi.fn(async () => respond()))
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('ranks campaigns with their rates, each linking to its detail page', async () => {
  const report: CampaignReport = {
    campaigns: [
      {
        id: 'c-1', name: 'Outbound Q3', status: 'running', created_at: '2026-07-01T00:00:00Z',
        ...counts({ sent: 4210, enrolled: 2000, opens: 1263, replies: 180, open_rate: 0.3, reply_rate: 0.09 }),
      },
      {
        id: 'c-2', name: 'Founder intros', status: 'done', created_at: '2026-06-01T00:00:00Z',
        ...counts({ sent: 300, enrolled: 300, replies: 12, reply_rate: 0.04 }),
      },
    ],
    totals: counts({ sent: 4510, enrolled: 2300, replies: 192, reply_rate: 192 / 2300 }),
  }
  respond = () => new Response(JSON.stringify(report), { status: 200, headers: jsonHeaders })

  renderWithProviders(<ReportsPage />)

  expect((await screen.findByText('Outbound Q3')).closest('a')).toHaveAttribute('href', '/app/campaigns/c-1')
  expect(screen.getByText('Founder intros').closest('a')).toHaveAttribute('href', '/app/campaigns/c-2')
  expect(screen.getByText('4,210')).toBeInTheDocument()
  expect(screen.getByText('30%')).toBeInTheDocument()
  // 9.0%, not 9% — anything under 10% keeps its decimal, so a 9.0 and a 9.4
  // don't collapse into the same cell.
  expect(screen.getByText('9.0%')).toBeInTheDocument()
  expect(screen.getByText('4.0%')).toBeInTheDocument()
})

// A 0.4% and a 0.9% reply rate are the difference between a campaign working
// and not; rounding both to "0%" (or to "1%") erases the only signal on the row.
test('keeps a decimal on small rates and drops it on large ones', async () => {
  const report: CampaignReport = {
    campaigns: [
      {
        id: 'c-1', name: 'Barely working', status: 'running', created_at: '2026-07-01T00:00:00Z',
        ...counts({ sent: 1000, enrolled: 1000, reply_rate: 0.004, open_rate: 0.42 }),
      },
    ],
    totals: counts(),
  }
  respond = () => new Response(JSON.stringify(report), { status: 200, headers: jsonHeaders })

  renderWithProviders(<ReportsPage />)

  expect(await screen.findByText('0.4%')).toBeInTheDocument()
  expect(screen.getByText('42%')).toBeInTheDocument()
})

// The whole point of the screen is trust in the numbers, so a failure must not
// leave a strip of zeros on screen — that reads as "every campaign is dead".
test('a failed load replaces the numbers instead of showing zeros beside an error', async () => {
  respond = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<ReportsPage />)

  expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't build this report/i)
  expect(screen.queryByText(/by campaign/i)).not.toBeInTheDocument()
  expect(screen.queryByText('Sent')).not.toBeInTheDocument()
})

test('a 403 names the missing scope, since no retry will fix it', async () => {
  respond = () => new Response(JSON.stringify({ error: 'forbidden' }), { status: 403, headers: jsonHeaders })

  renderWithProviders(<ReportsPage />)

  expect(await screen.findByRole('alert')).toHaveTextContent(/campaigns:read/)
})

test('an empty workspace explains itself rather than rendering an empty table', async () => {
  renderWithProviders(<ReportsPage />)

  expect(await screen.findByText(/nothing to compare yet/i)).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /go to campaigns/i })).toHaveAttribute('href', '/app/campaigns')
})

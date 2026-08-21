import { screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { OverviewPage } from '../overview-page'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, params: _params, ...props }: { to: string; children: React.ReactNode; params?: unknown }) => (
    <a href={to} {...props}>{children}</a>
  ),
}))

const headers = { 'content-type': 'application/json' }

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (url.includes('/mailboxes')) {
      return new Response(JSON.stringify([
        { id: 'mb-1', email: 'sender@example.com', status: 'active', daily_cap: 75 },
      ]), { status: 200, headers })
    }
    if (url.includes('/warmup/overview')) {
      return new Response(JSON.stringify({
        pool_size: 1,
        active: false,
        mailboxes: [{
          mailbox_id: 'mb-1',
          email: 'sender@example.com',
          enabled: true,
          health_state: 'healthy',
          health_reason: '',
          today_sent: 2,
          today_target: 5,
          inbox_rate_7d: 1,
          spam_rate_7d: 0,
        }],
      }), { status: 200, headers })
    }
    if (url.includes('/campaigns')) {
      return new Response(JSON.stringify([
        { id: 'c-1', name: 'Founder outreach', subject: 'A quick idea', status: 'running', stats: { sent: 12 } },
      ]), { status: 200, headers })
    }
    if (url.includes('/lists')) {
      return new Response(JSON.stringify([{ id: 'l-1', name: 'Founders' }]), { status: 200, headers })
    }
    return new Response(JSON.stringify({ error: 'unhandled' }), { status: 404, headers })
  }))
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('summarizes live workspace data without fabricated metrics', async () => {
  renderWithProviders(<OverviewPage />, {
    preloadedState: { auth: { userName: 'Ava Stone', status: 'authed' } },
  })

  expect(await screen.findByText('Good to see you, Ava.')).toBeInTheDocument()
  expect(await screen.findByText('75')).toBeInTheDocument()
  expect(await screen.findByText('Founder outreach')).toBeInTheDocument()
  expect(await screen.findByText('100%')).toBeInTheDocument()
  expect(screen.getByText('Nothing urgent')).toBeInTheDocument()
})

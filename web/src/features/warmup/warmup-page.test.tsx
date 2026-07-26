import { screen } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { WarmupPage } from './warmup-page'

const jsonHeaders = { 'content-type': 'application/json' }

let overviewResponder: () => Response
let mailboxesResponder: () => Response

beforeEach(() => {
  overviewResponder = () =>
    new Response(JSON.stringify({ pool_size: 1, active: false, mailboxes: [] }), { status: 200, headers: jsonHeaders })
  mailboxesResponder = () =>
    new Response(
      JSON.stringify([
        { id: 'mb-1', email: 'a@example.com', provider: 'gmail', status: 'active', daily_cap: 50 },
        { id: 'mb-2', email: 'b@example.com', provider: 'gmail', status: 'active', daily_cap: 50 },
      ]),
      { status: 200, headers: jsonHeaders },
    )

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/warmup/overview')) return overviewResponder()
      return mailboxesResponder()
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('shows the "needs at least 2 mailboxes" notice when the pool is inactive', async () => {
  renderWithProviders(<WarmupPage />)

  expect(await screen.findByText(/warmup needs at least 2 mailboxes/i)).toBeInTheDocument()
  // The pool-size summary reflects the single enrolled participant and idle state.
  expect(screen.getByText(/idle — needs 2\+/i)).toBeInTheDocument()
})

test('does not show the inactive notice once the pool is active', async () => {
  overviewResponder = () =>
    new Response(
      JSON.stringify({
        pool_size: 2,
        active: true,
        mailboxes: [
          { mailbox_id: 'mb-1', email: 'a@example.com', enabled: true, health_state: 'healthy', health_reason: '', today_sent: 2, today_target: 4, inbox_rate_7d: 0.9, spam_rate_7d: 0.1 },
        ],
      }),
      { status: 200, headers: jsonHeaders },
    )

  renderWithProviders(<WarmupPage />)

  expect(await screen.findByText(/exchanging mail/i)).toBeInTheDocument()
  expect(screen.queryByText(/warmup needs at least 2 mailboxes/i)).not.toBeInTheDocument()
})

test('surfaces an error banner when the overview request fails', async () => {
  overviewResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<WarmupPage />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/couldn't load the warmup overview/i)
})

test('shows the no-mailboxes empty state when there are none to warm', async () => {
  mailboxesResponder = () => new Response(JSON.stringify([]), { status: 200, headers: jsonHeaders })

  renderWithProviders(<WarmupPage />)

  expect(await screen.findByText(/no mailboxes to warm/i)).toBeInTheDocument()
})

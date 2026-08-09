import { fireEvent, screen } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { Mailbox, WarmupMailbox } from '@/store/api'
import { WarmupMailboxCard } from './warmup-mailbox-card'

const jsonHeaders = { 'content-type': 'application/json' }

const mailbox: Mailbox = {
  id: 'mb-1',
  email: 'a@example.com',
  provider: 'gmail',
  status: 'active',
  daily_cap: 50,
}

const entry: WarmupMailbox = {
  mailbox_id: 'mb-1',
  email: 'a@example.com',
  enabled: true,
  health_state: 'healthy',
  health_reason: '',
  today_sent: 2,
  today_target: 4,
  placement_sample_7d: 10,
  inbox_rate_7d: 0.9,
  spam_rate_7d: 0.1,
}

// Enrolled cards fetch detail (GET) for the sparkline/settings prefill and issue
// a DELETE to disable; steer each by method so we can fail only the disable call.
let disableResponder: () => Response

beforeEach(() => {
  disableResponder = () => new Response(null, { status: 204, headers: jsonHeaders })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // fetchBaseQuery passes a Request object, so the method lives there — fall
      // back to init for direct (url, init) calls.
      const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase()
      if (method === 'DELETE') return disableResponder()
      // Detail GET — minimal payload; series is short so the sparkline shows its
      // "not enough history" fallback rather than a chart.
      return new Response(
        JSON.stringify({
          participant: {
            mailbox_id: 'mb-1',
            enabled: true,
            start_volume: 4,
            max_volume: 40,
            ramp_increment: 2,
            reply_rate: 0.3,
            health_state: 'healthy',
            health_reason: '',
            started_at: '2026-07-26T00:00:00Z',
            today_sent: 2,
            today_target: 4,
          },
          series: [],
        }),
        { status: 200, headers: jsonHeaders },
      )
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('an empty placement window is shown as not measured', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{ ...entry, health_state: 'unknown', placement_sample_7d: 0, inbox_rate_7d: null, spam_rate_7d: null }}
    />,
  )

  expect(screen.getAllByText('Not measured')).toHaveLength(2)
  expect(screen.getByText('0 observations')).toBeInTheDocument()
  expect(screen.getByText('Needs evidence')).toBeInTheDocument()
})

test('a failed disable surfaces the inline error alert with the generic copy', async () => {
  disableResponder = () =>
    new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={entry} />)

  fireEvent.click(screen.getByRole('button', { name: /^disable$/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/couldn't disable warmup\. please try again\./i)
})

test('a 404 disable explains the mailbox is no longer a participant', async () => {
  disableResponder = () =>
    new Response(JSON.stringify({ error: 'not found' }), { status: 404, headers: jsonHeaders })

  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={entry} />)

  fireEvent.click(screen.getByRole('button', { name: /^disable$/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/no longer a warmup participant/i)
})

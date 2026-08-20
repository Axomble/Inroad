import { screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { CampaignEnrollmentsList } from '../campaign-enrollments-list'
import type { CampaignEnrollment } from '../api'

const jsonHeaders = { 'content-type': 'application/json' }

// Per-test responder for GET /campaigns/{id}/enrollments — stubbed at the
// fetch layer (same approach as mailboxes-page.test.tsx) so the real injected
// RTK query, cache, and hook run end-to-end rather than a mocked hook.
let enrollmentsResponder: () => Response

beforeEach(() => {
  enrollmentsResponder = () => new Response(JSON.stringify([]), { status: 200, headers: jsonHeaders })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/enrollments')) return enrollmentsResponder()
      return new Response(null, { status: 404 })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function makeEnrollment(overrides: Partial<CampaignEnrollment> = {}): CampaignEnrollment {
  return {
    email: 'lead@example.com',
    first_name: 'Lead',
    status: 'active',
    reply_class: null,
    reply_source: null,
    replied_at: null,
    ...overrides,
  }
}

describe('CampaignEnrollmentsList', () => {
  test('renders a row per enrollment with the classified reply pill', async () => {
    enrollmentsResponder = () =>
      new Response(
        JSON.stringify([
          makeEnrollment({ email: 'ada@example.com', first_name: 'Ada', reply_class: 'positive', replied_at: '2026-07-20T10:00:00Z' }),
          makeEnrollment({ email: 'bob@example.com', first_name: 'Bob', reply_class: 'unsubscribe', replied_at: '2026-07-21T10:00:00Z' }),
          makeEnrollment({ email: 'cid@example.com', first_name: 'Cid', reply_class: null, status: 'sending' }),
        ]),
        { status: 200, headers: jsonHeaders },
      )

    renderWithProviders(<CampaignEnrollmentsList campaignId="c-1" />)

    expect(await screen.findByText('ada@example.com')).toBeInTheDocument()
    expect(screen.getByText('bob@example.com')).toBeInTheDocument()
    expect(screen.getByText('cid@example.com')).toBeInTheDocument()

    // The pill's text label is the primary (colorblind-safe) signal.
    expect(screen.getByText('Positive')).toBeInTheDocument()
    expect(screen.getByText('Unsubscribed')).toBeInTheDocument()
    // The un-replied contact gets no pill and an em-dash for replied-at.
    expect(screen.queryByText('Neutral')).not.toBeInTheDocument()
    expect(screen.getAllByText('—').length).toBeGreaterThan(0)
  })

  test('shows the empty state when there are no enrollments', async () => {
    enrollmentsResponder = () => new Response(JSON.stringify([]), { status: 200, headers: jsonHeaders })

    renderWithProviders(<CampaignEnrollmentsList campaignId="c-1" />)

    expect(await screen.findByText('No enrollments yet')).toBeInTheDocument()
  })

  test('surfaces a typed error message when the request fails', async () => {
    enrollmentsResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

    renderWithProviders(<CampaignEnrollmentsList campaignId="c-1" />)

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent(/Couldn't load contacts/i)
  })
})

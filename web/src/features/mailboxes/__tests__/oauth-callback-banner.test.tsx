import { fireEvent, screen } from '@testing-library/react'
import { vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { api } from '@/store/api'
import { OauthCallbackBanner } from '../oauth-callback-banner'

// OauthCallbackBanner reads ?connected / ?oauth_error via getRouteApi and
// strips them via useNavigate — stub both the same way the auth page tests do.
let searchParams: { connected?: string; oauth_error?: string; provider?: 'gmail' | 'm365' } = {}
const navigateMock = vi.fn()

vi.mock('@tanstack/react-router', () => ({
  getRouteApi: () => ({ useSearch: () => searchParams }),
  useNavigate: () => navigateMock,
}))

afterEach(() => {
  searchParams = {}
  navigateMock.mockClear()
})

test('a successful Gmail connect shows the email, invalidates the list, and strips the query', () => {
  // The backend now always tags the redirect with &provider so the banner can
  // pick provider-correct copy.
  searchParams = { connected: 'sender@gmail.com', provider: 'gmail' }
  const invalidateSpy = vi.spyOn(api.util, 'invalidateTags')

  renderWithProviders(<OauthCallbackBanner />)

  expect(screen.getByRole('status')).toHaveTextContent(/Gmail mailbox sender@gmail\.com connected\./i)
  // New Gmail row won't be in the cached list — the banner refetches it.
  expect(invalidateSpy.mock.calls.some((args) => JSON.stringify(args).includes('Mailbox'))).toBe(true)
  // The query params are stripped so a refresh can't re-show the banner.
  expect(navigateMock).toHaveBeenCalledWith(
    expect.objectContaining({ to: '/app/mailboxes', replace: true, search: expect.any(Function) }),
  )
  // ...and stripped *selectively*: the same route carries the list's ?q=/?sort=,
  // so clearing the callback params must not reset the user's filter.
  const [{ search }] = navigateMock.mock.calls.at(-1) as [
    { search: (prev: Record<string, unknown>) => Record<string, unknown> },
  ]
  expect(search({ connected: 'x', provider: 'gmail', oauth_error: 'y', q: 'alex', sort: 'email' })).toEqual({
    connected: undefined,
    oauth_error: undefined,
    provider: undefined,
    q: 'alex',
    sort: 'email',
  })
})

test('a successful Microsoft 365 connect shows the Microsoft 365 label', () => {
  searchParams = { connected: 'rep@example.com', provider: 'm365' }

  renderWithProviders(<OauthCallbackBanner />)

  expect(screen.getByRole('status')).toHaveTextContent(/Microsoft 365 mailbox rep@example\.com connected\./i)
})

test('a Microsoft 365 disabled error names Microsoft 365', () => {
  searchParams = { oauth_error: 'disabled', provider: 'm365' }

  renderWithProviders(<OauthCallbackBanner />)

  expect(screen.getByRole('alert')).toHaveTextContent(/Microsoft 365 connect isn't configured on this server\./i)
})

test('a known error reason maps to plain, provider-neutral copy', () => {
  searchParams = { oauth_error: 'denied' }

  renderWithProviders(<OauthCallbackBanner />)

  expect(screen.getByRole('alert')).toHaveTextContent(/Sign-in was cancelled\./i)
})

test('an unknown error reason falls back to the generic message', () => {
  searchParams = { oauth_error: 'wat' }

  renderWithProviders(<OauthCallbackBanner />)

  expect(screen.getByRole('alert')).toHaveTextContent(/Couldn't complete the connection — try again\./i)
})

test('the banner is dismissible', () => {
  searchParams = { connected: 'sender@gmail.com' }

  renderWithProviders(<OauthCallbackBanner />)

  fireEvent.click(screen.getByRole('button', { name: /dismiss/i }))
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
})

test('renders nothing without callback params', () => {
  searchParams = {}

  const { container } = renderWithProviders(<OauthCallbackBanner />)
  expect(container).toBeEmptyDOMElement()
})

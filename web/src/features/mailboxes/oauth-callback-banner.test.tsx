import { fireEvent, screen } from '@testing-library/react'
import { vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { api } from '@/store/api'
import { OauthCallbackBanner } from './oauth-callback-banner'

// OauthCallbackBanner reads ?connected / ?oauth_error via getRouteApi and
// strips them via useNavigate — stub both the same way the auth page tests do.
let searchParams: { connected?: string; oauth_error?: string } = {}
const navigateMock = vi.fn()

vi.mock('@tanstack/react-router', () => ({
  getRouteApi: () => ({ useSearch: () => searchParams }),
  useNavigate: () => navigateMock,
}))

afterEach(() => {
  searchParams = {}
  navigateMock.mockClear()
})

test('a successful connect shows the email, invalidates the list, and strips the query', () => {
  searchParams = { connected: 'sender@gmail.com' }
  const invalidateSpy = vi.spyOn(api.util, 'invalidateTags')

  renderWithProviders(<OauthCallbackBanner />)

  expect(screen.getByRole('status')).toHaveTextContent(/Gmail mailbox sender@gmail\.com connected\./i)
  // New Gmail row won't be in the cached list — the banner refetches it.
  expect(invalidateSpy.mock.calls.some((args) => JSON.stringify(args).includes('Mailbox'))).toBe(true)
  // The query params are stripped so a refresh can't re-show the banner.
  expect(navigateMock).toHaveBeenCalledWith(
    expect.objectContaining({ to: '/app/mailboxes', search: {}, replace: true }),
  )
})

test('a known error reason maps to plain copy', () => {
  searchParams = { oauth_error: 'denied' }

  renderWithProviders(<OauthCallbackBanner />)

  expect(screen.getByRole('alert')).toHaveTextContent(/Google sign-in was cancelled\./i)
})

test('an unknown error reason falls back to the generic message', () => {
  searchParams = { oauth_error: 'wat' }

  renderWithProviders(<OauthCallbackBanner />)

  expect(screen.getByRole('alert')).toHaveTextContent(/Could not complete the Google connection/i)
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

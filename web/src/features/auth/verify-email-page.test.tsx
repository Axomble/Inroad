import { screen } from '@testing-library/react'
import { vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { VerifyEmailPage } from './verify-email-page'

// VerifyEmailPage reads the ?token= search param via getRouteApi — stub it
// (and the Link it renders) the same way login-form.test.tsx stubs the
// router, so the mutation-on-mount behavior can be driven by a bare fetch stub.
let searchParams: { token?: string } = {}

vi.mock('@tanstack/react-router', () => ({
  getRouteApi: () => ({ useSearch: () => searchParams }),
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

afterEach(() => {
  vi.unstubAllGlobals()
})

test('reads ?token=, verifies on mount, and shows a success state', async () => {
  searchParams = { token: 'good-token' }
  const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
  vi.stubGlobal('fetch', fetchMock)

  renderWithProviders(<VerifyEmailPage />)

  expect(await screen.findByRole('heading', { name: /email verified/i })).toBeInTheDocument()
  const [request] = fetchMock.mock.calls[0] as unknown as [Request]
  expect(request.url).toContain('/auth/verify-email')
  expect(request.method).toBe('POST')
})

test('shows an error state when the token is invalid or expired', async () => {
  searchParams = { token: 'bad-token' }
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      new Response(JSON.stringify({ error: 'invalid or expired token' }), {
        status: 400,
        headers: { 'content-type': 'application/json' },
      }),
    ),
  )

  renderWithProviders(<VerifyEmailPage />)

  expect(await screen.findByRole('heading', { name: /invalid or expired/i })).toBeInTheDocument()
})

test('shows an error state immediately when there is no token', async () => {
  searchParams = {}

  renderWithProviders(<VerifyEmailPage />)

  expect(await screen.findByRole('heading', { name: /invalid or expired/i })).toBeInTheDocument()
})

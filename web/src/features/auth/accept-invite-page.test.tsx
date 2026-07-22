import { fireEvent, screen, waitFor } from '@testing-library/react'
import { vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { AcceptInvitePage } from './accept-invite-page'

// Same router stub as verify-email-page.test.tsx: AcceptInvitePage reads
// ?token= via getRouteApi and renders a Link, both stubbed here.
let searchParams: { token?: string } = {}

vi.mock('@tanstack/react-router', () => ({
  getRouteApi: () => ({ useSearch: () => searchParams }),
  useNavigate: () => () => {},
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

afterEach(() => {
  vi.unstubAllGlobals()
})

test('a 422 (password required for a new account) prompts for a password instead of declaring the invite dead', async () => {
  searchParams = { token: 'invite-token' }
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      new Response(JSON.stringify({ error: 'password required to create an account' }), {
        status: 422,
        headers: { 'content-type': 'application/json' },
      }),
    ),
  )

  renderWithProviders(<AcceptInvitePage />)

  // Submitting with no password is valid client-side (the field is optional
  // for existing accounts) — the backend is what tells us this email needs one.
  fireEvent.click(screen.getByRole('button', { name: /accept invite/i }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/set a password above/i)
  await waitFor(() => expect(screen.getByLabelText('Password')).toHaveFocus())
})

test('a non-422 error (e.g. invalid/expired token) shows the dead-invite message', async () => {
  searchParams = { token: 'bad-token' }
  vi.stubGlobal(
    'fetch',
    vi.fn(async () =>
      new Response(JSON.stringify({ error: 'invalid or expired invite' }), {
        status: 404,
        headers: { 'content-type': 'application/json' },
      }),
    ),
  )

  renderWithProviders(<AcceptInvitePage />)

  fireEvent.click(screen.getByRole('button', { name: /accept invite/i }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/invalid or has expired/i)
})

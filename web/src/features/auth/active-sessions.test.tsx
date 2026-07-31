import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { SessionInfo } from '@/store/api'
import { ActiveSessions } from './active-sessions'

// Radix AlertDialog drives open/close through pointer + keyboard events jsdom
// doesn't fully implement; polyfill what it touches so the confirm dialog can
// actually open under test (same shim the mailboxes dropdown test uses).
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

const soon = new Date(Date.now() + 7 * 86_400_000).toISOString()
const earlier = new Date(Date.now() - 3 * 86_400_000).toISOString()

const CURRENT: SessionInfo = {
  id: 's-current',
  workspace_id: 'w-1',
  user_agent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/120.0 Safari/537.36',
  ip: '203.0.113.7',
  created_at: earlier,
  expires_at: soon,
  current: true,
}
const OTHER: SessionInfo = {
  id: 's-other',
  workspace_id: 'w-1',
  user_agent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36',
  ip: '198.51.100.4',
  created_at: earlier,
  expires_at: soon,
  current: false,
}

// Mutable server state + per-test overridable responders.
let sessions: SessionInfo[]
let listResponder: () => Response
let revokeResponder: (id: string) => Response

beforeEach(() => {
  sessions = [structuredClone(CURRENT), structuredClone(OTHER)]

  listResponder = () => new Response(JSON.stringify({ sessions }), { status: 200, headers: jsonHeaders })
  revokeResponder = (id: string) => {
    sessions = sessions.filter((s) => s.id !== id)
    return new Response(null, { status: 204 })
  }

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // fetchBaseQuery calls fetch with a Request object, so the method lives on
      // `input`, not `init` — read both.
      const request = input instanceof Request ? input : null
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? request?.method ?? 'GET'

      if (url.includes('/auth/sessions/revoke-others')) {
        const revoked = sessions.filter((s) => !s.current).length
        sessions = sessions.filter((s) => s.current)
        return new Response(JSON.stringify({ revoked }), { status: 200, headers: jsonHeaders })
      }
      if (method === 'DELETE' && url.includes('/auth/sessions/')) {
        const id = url.split('/auth/sessions/')[1] ?? ''
        return revokeResponder(id)
      }
      return listResponder()
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renders both sessions with the current one badged and non-revocable', async () => {
  renderWithProviders(<ActiveSessions />)

  const other = await screen.findByText('Chrome on Windows')

  // Current session is badged and has no revoke control.
  expect(await screen.findByText('Chrome on macOS')).toBeInTheDocument()
  expect(screen.getByText('This device')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /revoke session on chrome on macos/i })).not.toBeInTheDocument()

  // The other session is revocable.
  expect(other).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /revoke session on chrome on windows/i })).toBeInTheDocument()
})

test('revoking a session invalidates the list so it disappears', async () => {
  renderWithProviders(<ActiveSessions />)

  const revokeBtn = await screen.findByRole('button', { name: /revoke session on chrome on windows/i })
  fireEvent.click(revokeBtn)

  await waitFor(() => expect(screen.queryByText('Chrome on Windows')).not.toBeInTheDocument())
  // The current session survives, and the empty-others state appears.
  expect(screen.getByText('Chrome on macOS')).toBeInTheDocument()
  expect(screen.getByText('No other active sessions')).toBeInTheDocument()
})

test('sign out everywhere else confirms, then reports the revoked count', async () => {
  renderWithProviders(<ActiveSessions />)

  // Wait for the list to load — the topbar button is disabled (a no-op click)
  // until there's another session to revoke.
  await screen.findByText('Chrome on Windows')
  fireEvent.click(screen.getByRole('button', { name: /sign out everywhere else/i }))

  const confirm = await screen.findByRole('button', { name: /sign out other sessions/i })
  fireEvent.click(confirm)

  const status = await screen.findByRole('status')
  expect(status).toHaveTextContent(/signed out 1 other session\./i)
  await waitFor(() => expect(screen.queryByText('Chrome on Windows')).not.toBeInTheDocument())
})

test('shows the empty-other state when only the current session exists', async () => {
  sessions = [structuredClone(CURRENT)]

  renderWithProviders(<ActiveSessions />)

  expect(await screen.findByText('No other active sessions')).toBeInTheDocument()
  // "Sign out everywhere else" is disabled with nothing else to revoke.
  expect(screen.getByRole('button', { name: /sign out everywhere else/i })).toBeDisabled()
})

test('surfaces an error state when the session list fails to load', async () => {
  listResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<ActiveSessions />)

  expect(await screen.findByText("Couldn't load your sessions")).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
})

test('surfaces a shared error banner when a revoke fails', async () => {
  revokeResponder = () => new Response(JSON.stringify({ error: 'nope' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<ActiveSessions />)

  const revokeBtn = await screen.findByRole('button', { name: /revoke session on chrome on windows/i })
  fireEvent.click(revokeBtn)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/couldn't revoke that session\. please try again\./i)
  // The row is still there — the failed revoke didn't drop it.
  expect(screen.getByText('Chrome on Windows')).toBeInTheDocument()
})

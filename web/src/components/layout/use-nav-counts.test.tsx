import { screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { WorkspacePulse } from '@/features/pulse/api'
import { useNavCounts } from './use-nav-counts'

const jsonHeaders = { 'content-type': 'application/json' }

const pulse: WorkspacePulse = {
  mailboxes: { total: 12, active: 9, paused: 1, error: 2 },
  warmup: { pool: 6, unknown: 0, healthy: 6, watch: 0, at_risk: 0 },
  campaigns: { total: 8, running: 3, draft: 4, paused: 1 },
  contacts: { total: 1243 },
  sending: { sent_today: 247, daily_cap: 600 },
  inbox: { unread: 0, interested: 0 },
  attention: [],
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn(async () => new Response(JSON.stringify(pulse), { status: 200, headers: jsonHeaders }))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Renders the hook's return value so assertions read like the sidebar does. */
function Probe() {
  const counts = useNavCounts()
  return (
    <ul>
      {Object.entries(counts).map(([route, count]) => (
        <li key={route}>{`${route}=${count ?? 'none'}`}</li>
      ))}
    </ul>
  )
}

test('derives all four nav counts from the pulse payload alone', async () => {
  renderWithProviders(<Probe />, {
    preloadedState: { auth: { status: 'authed', activeWorkspaceId: 'ws-1' } },
  })

  expect(await screen.findByText('/app/mailboxes=12')).toBeInTheDocument()
  expect(screen.getByText('/app/warmup=6')).toBeInTheDocument()
  expect(screen.getByText('/app/campaigns=8')).toBeInTheDocument()
  // Contacts finally has a real workspace-wide total.
  expect(screen.getByText('/app/contacts=1243')).toBeInTheDocument()

  // The old full-list fetches are gone: the ONLY request is the pulse
  // aggregate (the server resolves the workspace from the session).
  const urls = fetchMock.mock.calls.map((call) => {
    const input = call[0] as RequestInfo | URL
    return typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
  })
  expect(urls.length).toBeGreaterThan(0)
  for (const url of urls) {
    expect(url).toMatch(/\/pulse$/)
  }
})

test('reports no counts while the pulse query is skipped (no active workspace)', () => {
  renderWithProviders(<Probe />)

  expect(screen.getByText('/app/mailboxes=none')).toBeInTheDocument()
  expect(screen.getByText('/app/contacts=none')).toBeInTheDocument()
  expect(fetchMock).not.toHaveBeenCalled()
})

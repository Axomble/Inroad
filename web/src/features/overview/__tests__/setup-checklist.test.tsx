import { act } from 'react'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { makeTestStore, renderWithProviders } from '@/test/render-with-providers'
import { dismissSetupChecklist } from '@/store/slices/ui'
import type { WorkspacePulse } from '@/features/pulse/api'
import { SetupChecklist } from '../setup-checklist'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>{children}</a>
  ),
}))

const headers = { 'content-type': 'application/json' }

/** A brand-new workspace: every step open. Tests override what they exercise. */
function freshPulse(): WorkspacePulse {
  return {
    mailboxes: { total: 0, active: 0, paused: 0, error: 0 },
    warmup: { pool: 0, unknown: 0, healthy: 0, watch: 0, at_risk: 0, probation: 0, quarantine: 0 },
    campaigns: { total: 0, running: 0, draft: 0, paused: 0 },
    contacts: { total: 0 },
    sending: { sent_today: 0, daily_cap: 0 },
    inbox: { unread: 0, interested: 0 },
    attention: [],
  }
}

/** A workspace past activation: every step derives complete. */
function completePulse(): WorkspacePulse {
  const p = freshPulse()
  p.mailboxes = { total: 2, active: 2, paused: 0, error: 0 }
  p.warmup = { ...p.warmup, pool: 2, healthy: 2 }
  p.contacts = { total: 40 }
  p.campaigns = { total: 2, running: 1, draft: 1, paused: 0 }
  return p
}

let pulseResponder: () => Response
let domainsResponder: () => Response | Promise<Response>
let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  pulseResponder = () => new Response(JSON.stringify(freshPulse()), { status: 200, headers })
  domainsResponder = () => new Response(JSON.stringify([]), { status: 200, headers })
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (url.includes('/pulse')) return pulseResponder()
    if (url.includes('/sending-domains')) return domainsResponder()
    return new Response(JSON.stringify({ error: 'unhandled' }), { status: 404, headers })
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const authed = { auth: { status: 'authed' as const, activeWorkspaceId: 'ws-1' } }

function findPanel() {
  return screen.findByRole('region', { name: /setup checklist/i })
}

/**
 * Queries fulfilled, plus a commit tick — for asserting the panel is ABSENT,
 * where "still loading" and "derived complete" would otherwise be
 * indistinguishable nulls. A dismissed panel skips the domains request
 * entirely, so callers say how many fetches they expect.
 */
async function queriesSettled(expectedFetches = 2) {
  await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(expectedFetches))
  // fetch resolution → RTK fulfilled dispatch → React commit spans a few
  // microtasks; one act-wrapped macrotask hop settles them all.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function stepRow(title: RegExp) {
  const row = screen.getByText(title).closest('li')
  if (!row) throw new Error(`no step row for ${String(title)}`)
  return row
}

test('a fresh workspace shows all five open steps, and only the first carries the panel button', async () => {
  renderWithProviders(<SetupChecklist />, { preloadedState: authed })
  const panel = await findPanel()

  for (const title of [
    /connect a mailbox/i,
    /verify your sending domain/i,
    /start warmup/i,
    /import contacts/i,
    /launch your first campaign/i,
  ]) {
    expect(stepRow(title)).toHaveTextContent('To do:')
  }
  expect(within(panel).getByText('0/5 done')).toBeInTheDocument()

  // The single tactile CTA (rendered through Button asChild) belongs to the
  // first incomplete step; later steps get quiet text links to their surface.
  const ctas = panel.querySelectorAll('a[data-slot="button"]')
  expect(ctas).toHaveLength(1)
  expect(stepRow(/connect a mailbox/i)).toContainElement(ctas[0] as HTMLElement)
  expect(ctas[0]).toHaveAttribute('href', '/app/mailboxes')
  expect(within(stepRow(/import contacts/i)).getByRole('link')).toHaveAttribute('href', '/app/contacts')
})

const doneCases: Array<[string, RegExp, (p: WorkspacePulse) => void]> = [
  ['Connect a mailbox', /connect a mailbox/i, (p) => { p.mailboxes.total = 1 }],
  ['Start warmup', /start warmup/i, (p) => { p.warmup.pool = 2 }],
  ['Import contacts', /import contacts/i, (p) => { p.contacts.total = 1 }],
  ['Launch your first campaign', /launch your first campaign/i, (p) => { p.campaigns = { total: 1, running: 1, draft: 0, paused: 0 } }],
]

test.each(doneCases)('"%s" derives done from the pulse payload', async (_name, title, mutate) => {
  const payload = freshPulse()
  mutate(payload)
  pulseResponder = () => new Response(JSON.stringify(payload), { status: 200, headers })

  renderWithProviders(<SetupChecklist />, { preloadedState: authed })
  await findPanel()

  expect(stepRow(title)).toHaveTextContent('Done:')
  expect(screen.getByText('1/5 done')).toBeInTheDocument()
})

test('a still-drafted campaign does not count as launched', async () => {
  const payload = freshPulse()
  payload.campaigns = { total: 2, running: 0, draft: 2, paused: 0 }
  pulseResponder = () => new Response(JSON.stringify(payload), { status: 200, headers })

  renderWithProviders(<SetupChecklist />, { preloadedState: authed })
  await findPanel()

  expect(stepRow(/launch your first campaign/i)).toHaveTextContent('To do:')
})

test('the domain step derives done from a passing sending domain, not a merely-listed one', async () => {
  domainsResponder = () =>
    new Response(JSON.stringify([
      { domain: 'weak.example', state: 'failing' },
      { domain: 'good.example', state: 'passing' },
    ]), { status: 200, headers })

  renderWithProviders(<SetupChecklist />, { preloadedState: authed })
  await findPanel()
  expect(stepRow(/verify your sending domain/i)).toHaveTextContent('Done:')
})

test('a listed but unverified domain leaves the step open', async () => {
  domainsResponder = () =>
    new Response(JSON.stringify([{ domain: 'weak.example', state: 'failing' }]), { status: 200, headers })

  renderWithProviders(<SetupChecklist />, { preloadedState: authed })
  await findPanel()
  expect(stepRow(/verify your sending domain/i)).toHaveTextContent('To do:')
})

test('a pool of one narrates progress instead of a bare unchecked step', async () => {
  const payload = freshPulse()
  payload.warmup = { ...payload.warmup, pool: 1 }
  pulseResponder = () => new Response(JSON.stringify(payload), { status: 200, headers })

  renderWithProviders(<SetupChecklist />, { preloadedState: authed })
  await findPanel()

  const row = stepRow(/start warmup/i)
  expect(row).toHaveTextContent('To do:')
  expect(row).toHaveTextContent(/1 mailbox warming — enroll a second/i)
})

test('the panel unmounts when every step derives complete, regardless of dismissal state', async () => {
  pulseResponder = () => new Response(JSON.stringify(completePulse()), { status: 200, headers })
  domainsResponder = () =>
    new Response(JSON.stringify([{ domain: 'good.example', state: 'passing' }]), { status: 200, headers })

  renderWithProviders(<SetupChecklist />, { preloadedState: authed })
  await queriesSettled()
  expect(screen.queryByRole('region', { name: /setup checklist/i })).toBeNull()
})

test('dismissal hides the panel, persists in the ui slice, and holds across a remount', async () => {
  const store = makeTestStore(authed)
  const first = renderWithProviders(<SetupChecklist />, { store })
  await findPanel()

  fireEvent.click(screen.getByRole('button', { name: /hide setup checklist/i }))
  expect(screen.queryByRole('region', { name: /setup checklist/i })).toBeNull()
  // The flag lives in the ui slice — the one subtree redux-persist whitelists —
  // which is what makes the dismissal survive a reload.
  expect(store.getState().ui.setupChecklistDismissed).toBe(true)

  first.unmount()
  renderWithProviders(<SetupChecklist />, { store })
  await queriesSettled()
  expect(screen.queryByRole('region', { name: /setup checklist/i })).toBeNull()
})

test('a rehydrated dismissal keeps an incomplete checklist hidden from the first render', async () => {
  const store = makeTestStore(authed)
  store.dispatch(dismissSetupChecklist())

  renderWithProviders(<SetupChecklist />, { store })
  // Dismissed panels skip the domains request — only the pulse fires.
  await queriesSettled(1)
  expect(screen.queryByRole('region', { name: /setup checklist/i })).toBeNull()
  const urls = fetchMock.mock.calls.map((call) => String(call[0]))
  expect(urls.some((url) => url.includes('/sending-domains'))).toBe(false)
})

test('a failed domains read still renders the panel with the domain step open — never a faked checkmark', async () => {
  domainsResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers })

  renderWithProviders(<SetupChecklist />, { preloadedState: authed })
  const panel = await findPanel()

  expect(panel).toBeInTheDocument()
  expect(stepRow(/verify your sending domain/i)).toHaveTextContent('To do:')
})

test('while the domains read is in flight, a guaranteed-to-render panel reserves its space with a skeleton', async () => {
  // Fresh workspace: pulse-derived steps are open, so the panel WILL render —
  // the slot must hold its height instead of shoving the page down later.
  domainsResponder = () => new Promise<Response>(() => {})

  renderWithProviders(<SetupChecklist />, { preloadedState: authed })

  await waitFor(() =>
    expect(document.querySelector('[data-slot="setup-checklist-skeleton"]')).not.toBeNull(),
  )
  expect(screen.queryByRole('region', { name: /setup checklist/i })).toBeNull()
})

test('while the domains read is in flight with every pulse step done, nothing renders — no skeleton flashed at a finished workspace', async () => {
  pulseResponder = () => new Response(JSON.stringify(completePulse()), { status: 200, headers })
  domainsResponder = () => new Promise<Response>(() => {})

  renderWithProviders(<SetupChecklist />, { preloadedState: authed })
  await queriesSettled(1)

  expect(document.querySelector('[data-slot="setup-checklist-skeleton"]')).toBeNull()
  expect(screen.queryByRole('region', { name: /setup checklist/i })).toBeNull()
})

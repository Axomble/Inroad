import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { Campaign } from '@/store/api'
import { usePauseResume } from '../lifecycle-actions'
import { LifecycleMenu, CampaignStatusButton, PauseResumeDialog } from '../lifecycle-menu'

// Radix DropdownMenu/AlertDialog drive open/close through pointer + keyboard
// events jsdom doesn't fully implement; polyfill what they touch so the menu
// and confirm dialogs can actually open under test (same shim
// mailboxes-page.test.tsx and active-sessions.test.tsx use).
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

function makeCampaign(overrides: Partial<Campaign> = {}): Campaign {
  return { id: 'c-1', name: 'Q3 Outbound', subject: 'Quick question', status: 'draft', ...overrides }
}

/**
 * `LifecycleMenu` and `CampaignStatusButton` take a caller-owned `pauseResume`
 * controller rather than creating their own — this mirrors exactly how
 * `campaigns-page.tsx` (menu only) and `campaign-detail-layout.tsx` (button +
 * menu, sharing one controller) actually compose them, rather than testing
 * the menu in a shape no real caller uses.
 */
function Menu({ campaign }: { campaign: Campaign }) {
  const pauseResume = usePauseResume(campaign)
  return (
    <>
      <LifecycleMenu campaign={campaign} pauseResume={pauseResume} />
      <PauseResumeDialog campaign={campaign} pauseResume={pauseResume} />
    </>
  )
}

/** The detail topbar's actual composition: one shared controller, two triggers. */
function Topbar({ campaign }: { campaign: Campaign }) {
  const pauseResume = usePauseResume(campaign)
  return (
    <>
      <CampaignStatusButton campaign={campaign} pauseResume={pauseResume} />
      <LifecycleMenu campaign={campaign} pauseResume={pauseResume} />
      <PauseResumeDialog campaign={campaign} pauseResume={pauseResume} />
    </>
  )
}

type CapturedRequest = { method: string; url: string }
let requests: CapturedRequest[]
let pauseResponder: () => Response
let renameResponder: (name: string) => Response

beforeEach(() => {
  requests = []
  pauseResponder = () => new Response(null, { status: 204 })
  renameResponder = (name) => jsonResponse({ id: 'c-1', name, subject: 'Quick question', status: 'draft' })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // RTK Query passes a `Request` for POST/PUT/DELETE mutations.
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()
      const body = isRequest ? await input.clone().json().catch(() => undefined) : undefined
      requests.push({ method, url })

      if (url.endsWith('/pause') && method === 'POST') return pauseResponder()
      if (method === 'DELETE') return new Response(null, { status: 204 })
      if (method === 'POST' && url.endsWith('/resume')) return new Response(null, { status: 204 })
      if (method === 'PUT' && url.endsWith('/campaigns/c-1')) return renameResponder(body?.name)
      return new Response(null, { status: 204 })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Opens the row's overflow menu. Radix opens on keydown (Enter), not a bare click. */
async function openMenu() {
  const trigger = await screen.findByRole('button', { name: /actions for q3 outbound/i })
  fireEvent.keyDown(trigger, { key: 'Enter' })
}

async function openRename() {
  await openMenu()
  fireEvent.click(await screen.findByRole('menuitem', { name: /rename/i }))
  return screen.findByRole('alertdialog')
}

test('a running campaign shows Pause, not Resume or Delete', async () => {
  renderWithProviders(<Menu campaign={makeCampaign({ status: 'running' })} />)
  await openMenu()

  expect(await screen.findByRole('menuitem', { name: /pause campaign/i })).toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /resume campaign/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /delete campaign/i })).not.toBeInTheDocument()
})

test('a paused campaign shows Resume, not Pause', async () => {
  renderWithProviders(<Menu campaign={makeCampaign({ status: 'paused' })} />)
  await openMenu()

  expect(await screen.findByRole('menuitem', { name: /resume campaign/i })).toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /pause campaign/i })).not.toBeInTheDocument()
})

test('a draft campaign shows Delete', async () => {
  renderWithProviders(<Menu campaign={makeCampaign({ status: 'draft' })} />)
  await openMenu()

  expect(await screen.findByRole('menuitem', { name: /delete campaign/i })).toBeInTheDocument()
})

test('every status offers Rename…, and a finished campaign offers no lifecycle transition', async () => {
  renderWithProviders(<Menu campaign={makeCampaign({ status: 'done' })} />)
  await openMenu()

  expect(await screen.findByRole('menuitem', { name: /rename/i })).toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /pause campaign/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /resume campaign/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /delete campaign/i })).not.toBeInTheDocument()
})

test('resuming a paused campaign fires the mutation with no confirmation dialog', async () => {
  renderWithProviders(<Menu campaign={makeCampaign({ status: 'paused' })} />)
  await openMenu()

  fireEvent.click(await screen.findByRole('menuitem', { name: /resume campaign/i }))

  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  await waitFor(() =>
    expect(requests.some((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/resume'))).toBe(true),
  )
})

test('deleting a draft campaign requires dialog confirmation, naming the campaign, before the DELETE request fires', async () => {
  renderWithProviders(<Menu campaign={makeCampaign({ status: 'draft' })} />)
  await openMenu()
  fireEvent.click(await screen.findByRole('menuitem', { name: /delete campaign/i }))

  const dialog = await screen.findByRole('alertdialog')
  expect(within(dialog).getByText(/q3 outbound/i)).toBeInTheDocument()
  expect(requests.some((r) => r.method === 'DELETE')).toBe(false)

  fireEvent.click(within(dialog).getByRole('button', { name: /delete campaign/i }))

  await waitFor(() =>
    expect(requests.some((r) => r.method === 'DELETE' && r.url.endsWith('/campaigns/c-1'))).toBe(true),
  )
})

test('a 409 on pause renders the API error copy returned by the server', async () => {
  pauseResponder = () => jsonResponse({ error: 'campaign is not running' }, 409)

  renderWithProviders(<Menu campaign={makeCampaign({ status: 'running' })} />)
  await openMenu()
  fireEvent.click(await screen.findByRole('menuitem', { name: /pause campaign/i }))

  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: /^pause campaign$/i }))

  expect(await screen.findByText('campaign is not running')).toBeInTheDocument()
  // The confirm dialog closes once the (failed) request settles.
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
})

// Finding 1 (review): CampaignStatusButton and LifecycleMenu each used to call
// usePauseResume(campaign) independently — two mutation triggers, two confirm-
// dialog states, so both could open their own dialog and each fire its own
// POST /pause. `Topbar` shares one controller (as campaign-detail-layout.tsx
// now does); this proves the sharing actually forecloses the double dialog.
test('the dedicated Pause button and the overflow menu item share one confirm dialog and one mutation trigger — they cannot stack dialogs or double-fire', async () => {
  renderWithProviders(<Topbar campaign={makeCampaign({ status: 'running' })} />)

  // Open the confirm dialog from the dedicated button.
  fireEvent.click(screen.getByRole('button', { name: /^pause$/i }))
  expect(await screen.findAllByRole('alertdialog', { hidden: true })).toHaveLength(1)

  // Opening the overflow menu and selecting "Pause campaign" again must not
  // stack a second dialog — there is structurally only ever one to open.
  const trigger = screen.getByRole('button', { name: /actions for q3 outbound/i, hidden: true })
  fireEvent.keyDown(trigger, { key: 'Enter' })
  fireEvent.click(await screen.findByRole('menuitem', { name: /pause campaign/i, hidden: true }))

  expect(screen.getAllByRole('alertdialog', { hidden: true })).toHaveLength(1)

  // Confirming fires the pause mutation exactly once, not once per trigger.
  const dialog = screen.getByRole('alertdialog', { hidden: true })
  fireEvent.click(within(dialog).getByRole('button', { name: /^pause campaign$/i, hidden: true }))

  await waitFor(() =>
    expect(requests.filter((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/pause')).length).toBe(1),
  )
})

// Finding 2 (review): RenameDialog is always mounted (controlled `open`), and
// the mutation's `error` used to survive a cancel — reopening after a failed,
// cancelled rename showed last time's error banner before any new action.
test('a failed rename that is cancelled does not show its stale error banner on reopen', async () => {
  renameResponder = () => jsonResponse({ error: 'name already in use' }, 400)

  renderWithProviders(<Menu campaign={makeCampaign({ status: 'draft' })} />)
  let dialog = await openRename()

  fireEvent.change(within(dialog).getByRole('textbox', { name: /name/i }), { target: { value: 'New name' } })
  fireEvent.click(within(dialog).getByRole('button', { name: /^save$/i }))
  expect(await within(dialog).findByText('name already in use')).toBeInTheDocument()

  // Cancel closes the dialog without renaming.
  fireEvent.click(within(dialog).getByRole('button', { name: /cancel/i }))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())

  // Reopening must not show last attempt's error before anything is submitted.
  dialog = await openRename()
  expect(within(dialog).queryByText('name already in use')).not.toBeInTheDocument()
})

// Finding 3 (review): client-side validation mirroring the backend's 1..200
// had no covering test.
test('an empty rename name shows the client validation error and does not fire the mutation', async () => {
  renderWithProviders(<Menu campaign={makeCampaign({ status: 'draft' })} />)
  const dialog = await openRename()

  fireEvent.change(within(dialog).getByRole('textbox', { name: /name/i }), { target: { value: '' } })
  fireEvent.click(within(dialog).getByRole('button', { name: /^save$/i }))

  expect(await within(dialog).findByText(/name is required/i)).toBeInTheDocument()
  expect(requests.some((r) => r.method === 'PUT')).toBe(false)
})

test('a 201-character rename name shows the client validation error and does not fire the mutation', async () => {
  renderWithProviders(<Menu campaign={makeCampaign({ status: 'draft' })} />)
  const dialog = await openRename()

  fireEvent.change(within(dialog).getByRole('textbox', { name: /name/i }), { target: { value: 'a'.repeat(201) } })
  fireEvent.click(within(dialog).getByRole('button', { name: /^save$/i }))

  expect(await within(dialog).findByText(/name must be 200 characters or fewer/i)).toBeInTheDocument()
  expect(requests.some((r) => r.method === 'PUT')).toBe(false)
})

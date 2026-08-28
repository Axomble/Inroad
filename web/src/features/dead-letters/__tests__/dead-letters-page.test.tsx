import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { TaskDeadLetter } from '@/store/api'
import { DeadLettersPage } from '../dead-letters-page'

const jsonHeaders = { 'content-type': 'application/json' }

let responder: (url: string) => Response
/** Every list URL the component asked for, so the query arguments are assertable. */
let listUrls: string[]

function letter(overrides: Partial<TaskDeadLetter> = {}): TaskDeadLetter {
  return {
    id: 'dl-1',
    task_type: 'sequence:advance',
    payload: { workspace_id: 'ws-1', enrollment_id: 'e-1' },
    last_error: 'smtp 550 mailbox unavailable',
    attempt_count: 3,
    status: 'pending',
    created_at: new Date(Date.now() - 60_000).toISOString(),
    replayed_at: null,
    ...overrides,
  }
}

function list(letters: TaskDeadLetter[], nextCursor?: string): Response {
  // next_cursor is OMITTED on the last page, never null and never '' — absence is
  // the end-of-list signal the server promises, so the fixture must model absence.
  const body = nextCursor === undefined ? { items: letters } : { items: letters, next_cursor: nextCursor }
  return new Response(JSON.stringify(body), { status: 200, headers: jsonHeaders })
}

beforeEach(() => {
  listUrls = []
  responder = () => list([letter()])
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
      // LIST reads only. Recording every /dead-letters call would count a replay's own
      // POST as a refetch, which is how the cache-invalidation test first passed with
      // the invalidation removed.
      if (method === 'GET' && /\/dead-letters(\?|$)/.test(url)) listUrls.push(url)
      if (method !== 'GET') {
        return new Response(JSON.stringify(letter({ status: 'discarded' })), { status: 200, headers: jsonHeaders })
      }
      return responder(url)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('a failed task is listed with its type, attempts and final error', async () => {
  renderWithProviders(<DeadLettersPage />)

  expect(await screen.findByText('sequence:advance')).toBeInTheDocument()
  expect(screen.getByText(/3 attempts before giving up/)).toBeInTheDocument()
  expect(screen.getByText(/smtp 550 mailbox unavailable/)).toBeInTheDocument()
})

// The screen opens on the queue that needs a decision, not on everything ever
// dropped — an operator arriving here is triaging.
test('the default view is the untriaged queue', async () => {
  renderWithProviders(<DeadLettersPage />)

  await screen.findByText('sequence:advance')
  expect(listUrls[0]).toMatch(/status=pending/)
  expect(screen.getByRole('button', { name: 'Untriaged' })).toHaveAttribute('aria-pressed', 'true')
})

// 'all' is the ABSENCE of the parameter in the contract. Sending a sentinel would
// earn a 422 from a server that only knows the three real states.
test('the all filter omits the status parameter rather than inventing one', async () => {
  renderWithProviders(<DeadLettersPage />)
  await screen.findByText('sequence:advance')

  fireEvent.click(screen.getByRole('button', { name: 'All' }))

  await waitFor(() => {
    const latest = listUrls[listUrls.length - 1] ?? ''
    expect(latest).not.toMatch(/status=/)
  })
})

// An empty queue is good news and the copy says so — the state most likely to be
// written as a shrug.
test('an empty queue reads as reassurance, not as an absence', async () => {
  responder = () => list([])

  renderWithProviders(<DeadLettersPage />)
  // The unfiltered view: the default is the pending slice, which cannot claim the
  // whole queue is clear.
  fireEvent.click(await screen.findByRole('button', { name: 'All' }))

  expect(await screen.findByText(/nothing has been dropped/i)).toBeInTheDocument()
  expect(screen.getByText(/nothing has been silently dropped/i)).toBeInTheDocument()
})

// A filtered empty view must not claim the whole queue is clear.
test('an empty filtered view points at the other states', async () => {
  responder = () => list([])

  renderWithProviders(<DeadLettersPage />)
  await screen.findByText(/no untriaged tasks/i)

  expect(screen.getByText(/other states may still have rows/i)).toBeInTheDocument()
  expect(screen.queryByText(/nothing has been silently dropped/i)).not.toBeInTheDocument()
})

// THE failure this screen must never produce: a read error rendering as "nothing
// has failed", which tells an operator their mail is fine when nothing is known.
test('a failed read never renders as an empty, healthy queue', async () => {
  responder = () => new Response(JSON.stringify({ message: 'nope' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<DeadLettersPage />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/couldn't load failed tasks|queue/i)

  // No empty state of ANY kind. Asserting only on the unfiltered copy passed while
  // the filtered empty block rendered happily beneath the banner — the default
  // filter is pending, so that copy was never on screen to begin with.
  expect(screen.queryByText(/no untriaged tasks/i)).not.toBeInTheDocument()
  expect(screen.queryByText(/other states may still have rows/i)).not.toBeInTheDocument()
  expect(screen.queryByText(/nothing has been dropped/i)).not.toBeInTheDocument()
  expect(screen.queryByText(/nothing has been silently dropped/i)).not.toBeInTheDocument()
})

// The page size is FIXED. The old version multiplied it per page, which the service
// silently clamped at 200 — so the fifth page asked for 250, got 200, inferred
// "no more", and hid every row past the cap with no error.
test('the requested page size never grows', async () => {
  responder = () => list([letter()], 'cur-2')

  renderWithProviders(<DeadLettersPage />)
  await screen.findByText('sequence:advance')

  fireEvent.click(screen.getByRole('button', { name: 'Next' }))

  await waitFor(() => expect(listUrls.length).toBeGreaterThan(1))
  for (const url of listUrls) {
    expect(url).toMatch(/limit=50(&|$)/)
  }
})

test('Next follows the cursor the server handed back', async () => {
  responder = () => list([letter()], 'cur-2')

  renderWithProviders(<DeadLettersPage />)
  await screen.findByText('sequence:advance')

  fireEvent.click(screen.getByRole('button', { name: 'Next' }))

  await waitFor(() => expect(listUrls[listUrls.length - 1]).toMatch(/cursor=cur-2/))
})

// Previous walks the stack home, and the first page is a request with NO cursor —
// sending an empty one would be sending a malformed cursor the server now 400s.
test('Previous returns to a cursor-less first page', async () => {
  responder = () => list([letter()], 'cur-2')

  renderWithProviders(<DeadLettersPage />)
  await screen.findByText('sequence:advance')
  expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()

  fireEvent.click(screen.getByRole('button', { name: 'Next' }))
  await waitFor(() => expect(listUrls[listUrls.length - 1]).toMatch(/cursor=cur-2/))

  fireEvent.click(screen.getByRole('button', { name: 'Previous' }))

  // Asserted on being home rather than on the last URL: page one is already in the
  // RTK Query cache, so returning to it issues no request at all — a last-URL check
  // would read the still-cursored page-two fetch and fail for the wrong reason.
  await waitFor(() => expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled())

  // And the trip home must not have sent an EMPTY cursor. '' is a different cache
  // key, so it would have issued a real request — and the server 400s a malformed
  // cursor, which is exactly what sending the stack's floor verbatim would do.
  for (const url of listUrls) {
    expect(url).not.toMatch(/cursor=(&|$)/)
  }
})

// A cursor is valid only under the filter that minted it — the server answers a
// carried-over one with a 400 — so switching tabs must drop it AND the stack.
test('switching filters drops the cursor rather than carrying it to another queue', async () => {
  responder = () => list([letter()], 'cur-2')

  renderWithProviders(<DeadLettersPage />)
  await screen.findByText('sequence:advance')
  fireEvent.click(screen.getByRole('button', { name: 'Next' }))
  await waitFor(() => expect(listUrls[listUrls.length - 1]).toMatch(/cursor=cur-2/))

  fireEvent.click(screen.getByRole('button', { name: 'Discarded' }))

  await waitFor(() => {
    const latest = listUrls[listUrls.length - 1] ?? ''
    expect(latest).toMatch(/status=discarded/)
    expect(latest).not.toMatch(/cursor=/)
  })
  // The stack went with it: Previous must not offer a page from the queue the
  // operator just left.
  expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
})

// THE stranding bug. Triaging every row on page 2 empties it; a pager rendered only
// beside rows takes Previous away at that exact moment, leaving no way off the page
// under copy claiming the filter is clear. Same unreachable-rows failure this screen
// was rewritten to remove, reached from the other side.
test('a page that empties under you keeps Previous, and does not claim the filter is clear', async () => {
  // Page one has rows and a next page; page two comes back empty — every row that
  // was on it has been triaged away. Not driven by a filter switch, which resets the
  // history by design and would make this assert nothing.
  responder = (url) => (url.includes('cursor=cur-2') ? list([]) : list([letter()], 'cur-2'))

  renderWithProviders(<DeadLettersPage />)
  await screen.findByText('sequence:advance')
  fireEvent.click(screen.getByRole('button', { name: 'Next' }))

  expect(await screen.findByText(/nothing left on this page/i)).toBeInTheDocument()
  // Previous is still there and, once the fetch settles, still usable — the point.
  // It is disabled mid-fetch on purpose, so this waits rather than racing it.
  await waitFor(() => expect(screen.getByRole('button', { name: 'Previous' })).toBeEnabled())
  // The lie it must not tell: earlier pages may be full of untriaged tasks.
  expect(screen.queryByText(/other states may still have rows/i)).not.toBeInTheDocument()
})

// The 400 this PR added to the contract. Without recovery the refused cursor sits in
// state with nothing able to clear it, and the screen is stuck on a banner.
test('a refused cursor returns to the first page and says so', async () => {
  responder = (url) =>
    url.includes('cursor=')
      ? new Response(JSON.stringify({ message: 'page cursor is not valid for this list' }), {
          status: 400,
          headers: jsonHeaders,
        })
      : list([letter()], 'cur-2')

  renderWithProviders(<DeadLettersPage />)
  await screen.findByText('sequence:advance')
  fireEvent.click(screen.getByRole('button', { name: 'Next' }))

  // Recovered: back on page one with its rows, not stranded on an error banner.
  //
  // Queried inside waitFor rather than holding the node findByText returns: recovery
  // re-renders, React swaps the notice for a fresh element, and asserting on the
  // captured reference fails against a node that has already been detached.
  // Asserted on the RECOVERED notice specifically, not on its text. The 400 error
  // banner carries the same words, so a text query passes on the transient banner
  // mid-recovery and proves nothing — which is exactly what it did until a revert
  // that suppressed the notice failed to fail.
  const notice = await screen.findByRole('status')
  await waitFor(() => expect(notice).toHaveTextContent(/that page link expired/i))
  expect(screen.getByText('sequence:advance')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
  // No error banner: the operator is not asked to act on something already handled.
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

// The whole point of the tag wiring in api.ts: a successful action must refresh the
// list, or the operator stares at a row they have already dealt with. Nothing tested
// that the invalidation is actually connected.
test('a successful action refetches the list', async () => {
  responder = () => list([letter()])

  renderWithProviders(<DeadLettersPage />)
  await screen.findByText('sequence:advance')
  const before = listUrls.length

  fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Discard task' }))

  await waitFor(() => expect(listUrls.length).toBeGreaterThan(before))
})

// Absence of next_cursor is the end of the list. A full page is NOT — the old code
// read a full page as "more exist", which is the inference this replaced.
test('the last page offers no Next, even when it is exactly full', async () => {
  responder = () => list(Array.from({ length: 50 }, (_, i) => letter({ id: `dl-${i}` })))

  renderWithProviders(<DeadLettersPage />)
  await screen.findAllByText('sequence:advance')

  expect(screen.queryByRole('button', { name: 'Next' })).not.toBeInTheDocument()
})

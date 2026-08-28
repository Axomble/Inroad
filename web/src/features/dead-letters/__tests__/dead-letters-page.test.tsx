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

function list(letters: TaskDeadLetter[]): Response {
  return new Response(JSON.stringify({ dead_letters: letters }), { status: 200, headers: jsonHeaders })
}

beforeEach(() => {
  listUrls = []
  responder = () => list([letter()])
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/dead-letters')) listUrls.push(url)
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

test('switching filters resets paging rather than requesting four pages of a new queue', async () => {
  responder = () => list(Array.from({ length: 50 }, (_, i) => letter({ id: `dl-${i}` })))

  renderWithProviders(<DeadLettersPage />)
  await screen.findAllByText('sequence:advance')

  fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
  await waitFor(() => expect(listUrls[listUrls.length - 1]).toMatch(/limit=100/))

  fireEvent.click(screen.getByRole('button', { name: 'Discarded' }))

  await waitFor(() => {
    const latest = listUrls[listUrls.length - 1] ?? ''
    expect(latest).toMatch(/status=discarded/)
    expect(latest).toMatch(/limit=50/)
  })
})

// A short page means there is nothing more to ask for; offering the button anyway
// trains an operator to click something that never does anything.
test('a partial page offers no load-more', async () => {
  responder = () => list([letter()])

  renderWithProviders(<DeadLettersPage />)
  await screen.findByText('sequence:advance')

  expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
})

import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { TaskDeadLetter } from '@/store/api'
import { DeadLetterRow } from '../dead-letter-row'

const jsonHeaders = { 'content-type': 'application/json' }

let actionResponder: () => Response
/** Every non-GET request the row made, so "nothing was sent" is assertable. */
let writes: string[]

function letter(overrides: Partial<TaskDeadLetter> = {}): TaskDeadLetter {
  return {
    id: 'dl-1',
    task_type: 'inbox:pending_reply_send',
    payload: { workspace_id: 'ws-1', reply_id: 'r-1' },
    last_error: 'connection refused',
    attempt_count: 5,
    status: 'pending',
    created_at: new Date(Date.now() - 3_600_000).toISOString(),
    replayed_at: null,
    ...overrides,
  }
}

beforeEach(() => {
  writes = []
  actionResponder = () => new Response(JSON.stringify(letter({ status: 'replayed' })), { status: 200, headers: jsonHeaders })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      // fetchBaseQuery calls fetch(request) with a Request, so the method lives on
      // the input rather than in init — reading only init sees every POST as a GET.
      const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
      if (method !== 'GET') {
        writes.push(`${method} ${url}`)
        return actionResponder()
      }
      return new Response(JSON.stringify({ dead_letters: [] }), { status: 200, headers: jsonHeaders })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/* ------------------------------------------------------- what may be acted on */

// Terminal rows offer no actions at all. A disabled button would read as a missing
// permission rather than a decision that has already been made.
test('a replayed row offers no actions', () => {
  renderWithProviders(<DeadLetterRow letter={letter({ status: 'replayed', replayed_at: new Date().toISOString() })} />)

  expect(screen.queryByRole('button', { name: 'Replay' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Discard' })).not.toBeInTheDocument()
})

test('a discarded row offers no actions', () => {
  renderWithProviders(<DeadLetterRow letter={letter({ status: 'discarded' })} />)

  expect(screen.queryByRole('button', { name: 'Replay' })).not.toBeInTheDocument()
})

// A status this build cannot interpret is the case where acting could do the most
// damage, so it is treated as terminal rather than as pending.
test('an unrecognised status offers no actions', () => {
  renderWithProviders(<DeadLetterRow letter={letter({ status: 'quarantined' as TaskDeadLetter['status'] })} />)

  expect(screen.getByText('quarantined')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Replay' })).not.toBeInTheDocument()
})

/* --------------------------------------------------------------- confirmation */

// Replay puts mail on the wire. It must never be one click from the list.
test('replay confirms first, and says it delivers mail', async () => {
  renderWithProviders(<DeadLetterRow letter={letter()} />)

  fireEvent.click(screen.getByRole('button', { name: 'Replay' }))

  expect(await screen.findByText(/this delivers mail/i)).toBeInTheDocument()
  // Nothing has been sent yet — the dialog is a gate, not a progress indicator.
  expect(writes).toEqual([])
})

test('cancelling the dialog sends nothing', async () => {
  renderWithProviders(<DeadLetterRow letter={letter()} />)

  fireEvent.click(screen.getByRole('button', { name: 'Replay' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))

  await waitFor(() => expect(screen.queryByText(/this delivers mail/i)).not.toBeInTheDocument())
  expect(writes).toEqual([])
})

test('confirming replay posts to the replay endpoint exactly once', async () => {
  renderWithProviders(<DeadLetterRow letter={letter()} />)

  fireEvent.click(screen.getByRole('button', { name: 'Replay' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Replay task' }))

  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toMatch(/POST .*\/dead-letters\/dl-1\/replay/)
})

test('discard posts to the discard endpoint and warns it cannot be undone', async () => {
  renderWithProviders(<DeadLetterRow letter={letter()} />)

  fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
  expect(await screen.findByText(/cannot be undone/i)).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: 'Discard task' }))

  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toMatch(/POST .*\/dead-letters\/dl-1\/discard/)
})

/* ------------------------------------------------------------ failed actions */

// The exactly-once guarantee working. Telling the operator to try again here would
// be advising them to fight a race the server already settled.
test('a 409 is reported as already handled rather than as an error to retry', async () => {
  actionResponder = () => new Response(JSON.stringify({ message: 'already replayed' }), { status: 409, headers: jsonHeaders })

  renderWithProviders(<DeadLetterRow letter={letter()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Replay' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Replay task' }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/already handled/i)
  expect(alert).toHaveTextContent(/nothing was run twice/i)
})

// Permanent per the contract: the operator is pointed at discard, not at retrying.
test('a 422 says the row can never be replayed', async () => {
  actionResponder = () => new Response(JSON.stringify({ message: 'payload not replayable' }), { status: 422, headers: jsonHeaders })

  renderWithProviders(<DeadLetterRow letter={letter()} />)
  fireEvent.click(screen.getByRole('button', { name: 'Replay' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Replay task' }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/permanent/i)
})

/* ----------------------------------------------------------------- the payload */

// The payload is what replay re-runs, so an operator has to be able to see what
// they are about to re-send. Collapsed by default; a list of twenty raw payloads is
// unreadable.
test('the payload is hidden until asked for, then shown verbatim', async () => {
  renderWithProviders(<DeadLetterRow letter={letter()} />)

  expect(screen.queryByText(/reply_id/)).not.toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /payload/i }))

  expect(await screen.findByText(/"reply_id": "r-1"/)).toBeInTheDocument()
})

test('an empty last error is stated rather than left blank', () => {
  renderWithProviders(<DeadLetterRow letter={letter({ last_error: '' })} />)

  expect(screen.getByText(/no error text was recorded/i)).toBeInTheDocument()
})

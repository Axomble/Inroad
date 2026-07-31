import { fireEvent, screen, waitFor } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { SendersPanel } from './senders-panel'
// Importing the feature api registers the sender-pool endpoints + tag wiring on
// the shared endpoints registry, so the hooks resolve.
import './api'

const POOL = {
  rotation_mode: 'round_robin',
  senders: [
    {
      mailbox_id: 'mb-2',
      email: 'brooke@example.com',
      provider: 'gmail',
      status: 'active',
      weight: 7,
      enabled: true,
      assigned_count: 12,
      last_assigned_at: new Date(Date.now() - 3 * 3_600_000).toISOString(),
    },
  ],
}

const MAILBOXES = [
  { id: 'mb-1', email: 'alex@example.com', provider: 'smtp', status: 'active' },
  { id: 'mb-2', email: 'brooke@example.com', provider: 'gmail', status: 'active' },
]

type Stub = { putStatus?: number; putBody?: unknown; mailboxes?: unknown[]; pool?: unknown }

/** Stubs the mailbox list plus the pool GET, and the PUT with the given status. */
function stubSenders({ putStatus = 200, putBody, mailboxes = MAILBOXES, pool = POOL }: Stub = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const req = input as Request
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })

    if (req.url.endsWith('/mailboxes')) return json(mailboxes)
    if (!req.url.endsWith('/campaigns/c-1/senders')) return new Response(null, { status: 404 })
    if (req.method === 'PUT') {
      return putStatus === 200 ? json(pool) : json(putBody ?? { error: 'nope' }, putStatus)
    }
    return json(pool)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

const putCalls = (fetchMock: ReturnType<typeof stubSenders>) =>
  fetchMock.mock.calls.filter((c) => (c[0] as Request).method === 'PUT')

async function readPut(fetchMock: ReturnType<typeof stubSenders>) {
  await waitFor(() => expect(putCalls(fetchMock).length).toBeGreaterThan(0))
  const put = putCalls(fetchMock)[0]?.[0] as Request
  return (await put.json()) as {
    rotation_mode: string
    senders: { mailbox_id: string; weight: number; enabled: boolean }[]
  }
}

describe('SendersPanel', () => {
  test("renders the saved mode, the pool members, and each row's rotation state", async () => {
    stubSenders()
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Rotation mode')).toHaveValue('round_robin'))
    // The selected mode explains itself rather than showing a bare enum value.
    expect(screen.getByText(/fewest contacts so far/)).toBeInTheDocument()

    expect(screen.getByLabelText('Include brooke@example.com in the pool')).toBeChecked()
    expect(screen.getByLabelText('Weight for brooke@example.com')).toHaveValue(7)
    expect(screen.getByText(/12 contacts · last assigned 3 hours ago/)).toBeInTheDocument()

    // A workspace mailbox outside the pool is offered, unticked, at weight 1.
    expect(screen.getByLabelText('Include alex@example.com in the pool')).not.toBeChecked()
    expect(screen.getByText(/never assigned/)).toBeInTheDocument()
  })

  test('states that follow-ups stay on the mailbox that started the thread', async () => {
    stubSenders()
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Rotation mode')).toBeInTheDocument())
    expect(screen.getByText(/Follow-ups always send from the mailbox that started the thread/)).toBeInTheDocument()
    expect(screen.getByText(/not individual sends/)).toBeInTheDocument()
  })

  test('a failed load is surfaced, not shown as an empty pool', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const req = input as Request
        if (req.url.endsWith('/mailboxes')) {
          return new Response(JSON.stringify(MAILBOXES), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          })
        }
        return new Response(null, { status: 500 })
      }),
    )
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/Couldn't load the senders \(500\)/))
    expect(screen.queryByLabelText('Rotation mode')).not.toBeInTheDocument()
  })

  test('a workspace with nothing to send from says so instead of an empty list', async () => {
    stubSenders({ mailboxes: [], pool: { rotation_mode: 'weighted', senders: [] } })
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Rotation mode')).toBeInTheDocument())
    expect(screen.getByText(/No active mailboxes to send from/)).toBeInTheDocument()
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
  })

  test('a pool member missing from the mailbox list is still shown, not dropped', async () => {
    stubSenders({ mailboxes: [] })
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    // The PUT is a full replace, so a row silently omitted here would delete the
    // mailbox from the pool on the next save.
    await waitFor(() => expect(screen.getByLabelText('Include brooke@example.com in the pool')).toBeChecked())
    expect(screen.queryByText(/No active mailboxes to send from/)).not.toBeInTheDocument()
  })

  test('the save action only appears once something is edited', async () => {
    stubSenders()
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Rotation mode')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /save senders/i })).not.toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Include alex@example.com in the pool'))
    expect(screen.getByRole('button', { name: /save senders/i })).toBeInTheDocument()
  })

  test('sends the whole pool and the mode as a full replace', async () => {
    const fetchMock = stubSenders()
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Rotation mode')).toBeInTheDocument())
    fireEvent.click(screen.getByLabelText('Include alex@example.com in the pool'))
    fireEvent.change(screen.getByLabelText('Weight for alex@example.com'), { target: { value: '4' } })
    fireEvent.click(screen.getByLabelText('In rotation for brooke@example.com'))
    fireEvent.change(screen.getByLabelText('Rotation mode'), { target: { value: 'weighted' } })
    fireEvent.click(screen.getByRole('button', { name: /save senders/i }))

    const body = await readPut(fetchMock)
    expect(body.rotation_mode).toBe('weighted')
    expect(body.senders).toEqual([
      { mailbox_id: 'mb-1', weight: 4, enabled: true },
      // Held out of rotation, but still in the pool so its history survives.
      { mailbox_id: 'mb-2', weight: 7, enabled: false },
    ])
    await waitFor(() => expect(screen.getByText(/Senders saved/)).toBeInTheDocument())
  })

  test('excluding every mailbox blocks the save with an explanation', async () => {
    const fetchMock = stubSenders()
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Rotation mode')).toBeInTheDocument())
    fireEvent.click(screen.getByLabelText('Include brooke@example.com in the pool'))
    fireEvent.click(screen.getByRole('button', { name: /save senders/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/at least one mailbox/)
    // Nothing may be sent: a campaign with no senders can never send.
    expect(putCalls(fetchMock)).toHaveLength(0)
  })

  test('an out-of-range weight is caught client-side before any request', async () => {
    const fetchMock = stubSenders()
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Rotation mode')).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Weight for brooke@example.com'), { target: { value: '101' } })
    fireEvent.click(screen.getByRole('button', { name: /save senders/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/weight must be a whole number from 1 to 100/)
    expect(putCalls(fetchMock)).toHaveLength(0)
  })

  test('a cleared weight is caught client-side too', async () => {
    const fetchMock = stubSenders()
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Rotation mode')).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Weight for brooke@example.com'), { target: { value: '' } })
    fireEvent.click(screen.getByRole('button', { name: /save senders/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/weight must be a whole number/)
    expect(putCalls(fetchMock)).toHaveLength(0)
  })

  test("a rejected save surfaces the server's reason and keeps the edits", async () => {
    stubSenders({ putStatus: 422, putBody: { error: 'mailbox is not active' } })
    renderWithProviders(<SendersPanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Rotation mode')).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Weight for brooke@example.com'), { target: { value: '9' } })
    fireEvent.click(screen.getByRole('button', { name: /save senders/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /That sender pool was rejected: mailbox is not active/,
    )
    // The edit stays in the form so it can be corrected rather than retyped.
    expect(screen.getByLabelText('Weight for brooke@example.com')).toHaveValue(9)
  })
})

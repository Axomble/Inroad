import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { WarmupSettingsForm } from '../warmup-settings-form'

const jsonHeaders = { 'content-type': 'application/json' }
const PUT_URL = '/mailboxes/mb-1/warmup'

let putResponder: () => Response
let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  putResponder = () =>
    new Response(
      JSON.stringify({
        mailbox_id: 'mb-1',
        enabled: true,
        start_volume: 4,
        max_volume: 40,
        ramp_increment: 2,
        reply_rate: 0.3,
        health_state: 'healthy',
        health_reason: '',
        started_at: '2026-07-26T00:00:00Z',
        today_sent: 0,
        today_target: 4,
      }),
      { status: 200, headers: jsonHeaders },
    )

  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
    if (url.includes(PUT_URL)) return putResponder()
    return new Response('{}', { status: 200, headers: jsonHeaders })
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function setField(label: RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

function submit() {
  fireEvent.click(screen.getByRole('button', { name: /enable warmup/i }))
}

test('rejects start_volume greater than max_volume without calling the API', async () => {
  renderWithProviders(<WarmupSettingsForm mailboxId="mb-1" onDone={() => {}} onCancel={() => {}} />)

  setField(/start volume/i, '50')
  setField(/max volume/i, '10')
  submit()

  expect(await screen.findByText(/start volume can't exceed max volume/i)).toBeInTheDocument()
  // A rejected client-side validation must never reach the network. The form
  // issues no other requests, so any fetch call would be the blocked PUT.
  expect(fetchMock).not.toHaveBeenCalled()
})

test('rejects an out-of-range reply_rate', async () => {
  renderWithProviders(<WarmupSettingsForm mailboxId="mb-1" onDone={() => {}} onCancel={() => {}} />)

  setField(/reply rate/i, '2')
  submit()

  expect(await screen.findByText(/at most 1/i)).toBeInTheDocument()
})

test('a valid submit enables warmup and calls onDone', async () => {
  const onDone = vi.fn()
  renderWithProviders(<WarmupSettingsForm mailboxId="mb-1" onDone={onDone} onCancel={() => {}} />)

  setField(/start volume/i, '4')
  setField(/max volume/i, '40')
  setField(/ramp increment/i, '2')
  setField(/reply rate/i, '0.3')
  submit()

  // onDone only fires on a successful `data` result, so reaching it proves the
  // PUT round-tripped.
  await waitFor(() => expect(onDone).toHaveBeenCalled())
  expect(fetchMock).toHaveBeenCalled()
})

test('surfaces a server validation error inline and keeps the form open', async () => {
  putResponder = () =>
    new Response(JSON.stringify({ error: 'start_volume > max_volume' }), { status: 400, headers: jsonHeaders })
  const onDone = vi.fn()

  renderWithProviders(<WarmupSettingsForm mailboxId="mb-1" onDone={onDone} onCancel={() => {}} />)

  setField(/start volume/i, '4')
  setField(/max volume/i, '40')
  submit()

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/those settings were rejected/i)
  expect(onDone).not.toHaveBeenCalled()
})

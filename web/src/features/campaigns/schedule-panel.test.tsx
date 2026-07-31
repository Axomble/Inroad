import { fireEvent, screen, waitFor } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { SchedulePanel } from './schedule-panel'
// Importing the feature api registers the schedule endpoints + tag wiring on the
// shared endpoints registry, so the hooks resolve.
import './api'

const SCHEDULE = {
  timezone: 'America/New_York',
  days: [
    { weekday: 1, intervals: [{ start_minute: 540, end_minute: 1020 }] },
    { weekday: 3, intervals: [{ start_minute: 600, end_minute: 780 }] },
  ],
  daily_limit: 250,
  preview: ['Mon 09:14:37', 'Mon 10:02:11', 'Mon 11:47:03'],
}

/** Stubs fetch for the schedule GET, and PUT with the given status. */
function stubSchedule({
  putStatus = 200,
  schedule = SCHEDULE as Record<string, unknown>,
}: { putStatus?: number; schedule?: Record<string, unknown> } = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const req = input as Request
    if (!req.url.endsWith('/campaigns/c-1/schedule')) {
      return new Response(null, { status: 404 })
    }
    if (req.method === 'PUT') {
      const body = putStatus === 200 ? JSON.stringify(schedule) : JSON.stringify({ error: 'nope' })
      return new Response(body, { status: putStatus, headers: { 'content-type': 'application/json' } })
    }
    return new Response(JSON.stringify(schedule), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

/** The body of the first PUT, once one has been made. */
async function readPut(fetchMock: ReturnType<typeof stubSchedule>) {
  await waitFor(() => {
    expect(fetchMock.mock.calls.some((c) => (c[0] as Request).method === 'PUT')).toBe(true)
  })
  const put = fetchMock.mock.calls.find((c) => (c[0] as Request).method === 'PUT')?.[0] as Request
  return (await put.json()) as { timezone: string; days: { weekday: number }[]; daily_limit: number | null }
}

describe('SchedulePanel', () => {
  test('renders the saved windows, timezone, and the send-time preview', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Timezone')).toHaveValue('America/New_York'))
    expect(screen.getByLabelText('Mon window 1 start')).toHaveValue('09:00')
    expect(screen.getByLabelText('Mon window 1 end')).toHaveValue('17:00')
    expect(screen.getByLabelText('Wed window 1 start')).toHaveValue('10:00')
    // Closed days say so rather than rendering empty rows.
    expect(screen.getAllByText('No sending').length).toBe(5)
    expect(screen.getByText(/Mon 09:14:37/)).toBeInTheDocument()
  })

  test('a failed load is surfaced, not shown as an empty schedule', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(null, { status: 500 })),
    )
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/Couldn't load the schedule/))
    expect(screen.queryByLabelText('Timezone')).not.toBeInTheDocument()
  })

  test('the save action only appears once something is edited', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /save schedule/i })).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Mon window 1 start'), { target: { value: '10:30' } })
    expect(screen.getByRole('button', { name: /save schedule/i })).toBeInTheDocument()
  })

  test('sends the edited schedule as a full replace', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Mon window 1 start'), { target: { value: '10:30' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    await waitFor(() => {
      const put = fetchMock.mock.calls.find((c) => (c[0] as Request).method === 'PUT')
      expect(put).toBeDefined()
    })
    const put = fetchMock.mock.calls.find((c) => (c[0] as Request).method === 'PUT')?.[0] as Request
    const body = (await put.json()) as { timezone: string; days: { weekday: number }[] }
    expect(body.timezone).toBe('America/New_York')
    expect(body.days.map((d) => d.weekday)).toEqual([1, 3])
    await waitFor(() => expect(screen.getByText(/Schedule saved/)).toBeInTheDocument())
  })

  test('adding a window to a closed day makes it sendable', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /add a fri window/i }))
    expect(screen.getByLabelText('Fri window 1 start')).toHaveValue('09:00')

    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))
    await waitFor(() => {
      const put = fetchMock.mock.calls.find((c) => (c[0] as Request).method === 'PUT')
      expect(put).toBeDefined()
    })
    const put = fetchMock.mock.calls.find((c) => (c[0] as Request).method === 'PUT')?.[0] as Request
    const body = (await put.json()) as { days: { weekday: number }[] }
    expect(body.days.map((d) => d.weekday)).toContain(5)
  })

  test('removing the last window blocks the save with an explanation', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /remove mon window 1/i }))
    fireEvent.click(screen.getByRole('button', { name: /remove wed window 1/i }))
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/at least one sending window/)
    // Nothing may be sent: an all-closed week would park every enrollment.
    expect(fetchMock.mock.calls.some((c) => (c[0] as Request).method === 'PUT')).toBe(false)
  })

  test('an invalid range is caught client-side before any request', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Mon window 1 end'), { target: { value: '08:00' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/end time must be after the start time/)
    expect(fetchMock.mock.calls.some((c) => (c[0] as Request).method === 'PUT')).toBe(false)
  })

  test("a rejected save surfaces the server's reason and keeps the edits", async () => {
    stubSchedule({ putStatus: 422 })
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Mon window 1 start'), { target: { value: '10:30' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/That schedule is invalid/)
    // The edit stays in the form so it can be corrected rather than retyped.
    expect(screen.getByLabelText('Mon window 1 start')).toHaveValue('10:30')
  })

  test('renders the saved daily limit and says it is campaign-wide, not per mailbox', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Daily limit')).toHaveValue(250))
    // The whole point of the copy: read as per-mailbox, an operator configures a
    // pool of 5 to a fifth of the volume they intended.
    expect(screen.getByText(/every sender in its pool/)).toBeInTheDocument()
    expect(screen.getByText(/not per mailbox/)).toBeInTheDocument()
  })

  test('no campaign limit shows as an empty field, not as a zero', async () => {
    stubSchedule({ schedule: { ...SCHEDULE, daily_limit: null } })
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    expect(screen.getByLabelText('Daily limit')).toHaveValue(null)
  })

  test('a field the server omitted entirely still renders empty', async () => {
    const { daily_limit: _omitted, ...withoutLimit } = SCHEDULE
    stubSchedule({ schedule: withoutLimit })
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    expect(screen.getByLabelText('Daily limit')).toHaveValue(null)
  })

  test('a typed limit is sent as a number', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Daily limit')).toHaveValue(250))
    fireEvent.change(screen.getByLabelText('Daily limit'), { target: { value: '80' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    expect((await readPut(fetchMock)).daily_limit).toBe(80)
  })

  test('clearing the limit sends null, which is how the limit is removed', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Daily limit')).toHaveValue(250))
    fireEvent.change(screen.getByLabelText('Daily limit'), { target: { value: '' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    // Omitting the field would leave the old limit in place on a full-replace PUT.
    const body = await readPut(fetchMock)
    expect(body.daily_limit).toBeNull()
    expect('daily_limit' in body).toBe(true)
  })

  test('a limit below 1 is refused client-side with no request', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Daily limit')).toHaveValue(250))
    fireEvent.change(screen.getByLabelText('Daily limit'), { target: { value: '0' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/1 or more/)
    expect(fetchMock.mock.calls.some((c) => (c[0] as Request).method === 'PUT')).toBe(false)
  })

  test('editing only the limit is enough to enable the save', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('Daily limit')).toHaveValue(250))
    expect(screen.queryByRole('button', { name: /save schedule/i })).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Daily limit'), { target: { value: '300' } })
    expect(screen.getByRole('button', { name: /save schedule/i })).toBeInTheDocument()
  })

  test('the preview is withheld while edits are unsaved', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByText(/Mon 09:14:37/)).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Mon window 1 start'), { target: { value: '10:30' } })

    // Showing the saved schedule's send times next to unsaved edits would claim a
    // cadence that isn't in effect.
    expect(screen.queryByText(/Mon 09:14:37/)).not.toBeInTheDocument()
    expect(screen.getByText(/Save to see the send times/)).toBeInTheDocument()
  })
})

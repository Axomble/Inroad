import { fireEvent, screen, waitFor } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { SchedulePanel } from '../schedule-panel'
// Importing the feature api registers the schedule endpoints + tag wiring on the
// shared endpoints registry, so the hooks resolve.
import '../api'

const SCHEDULE = {
  timezone: 'America/New_York',
  days: [
    { weekday: 1, intervals: [{ start_minute: 540, end_minute: 1020 }] },
    { weekday: 3, intervals: [{ start_minute: 600, end_minute: 780 }] },
  ],
  daily_limit: 250,
  max_new_leads_per_day: 25,
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

/**
 * Switches to the "Exact times" editor and waits for it.
 *
 * The board is now the default view, and the time inputs are UNMOUNTED while it
 * shows — not merely hidden — so a test that wants them must ask for them, the
 * same way an operator does. Every test below that reads or edits a time input
 * goes through here.
 */
async function showTimeInputs() {
  fireEvent.click(await screen.findByRole('button', { name: /exact times/i }))
  await screen.findByLabelText('Mon window 1 start')
}

/** The body of the first PUT, once one has been made. */
async function readPut(fetchMock: ReturnType<typeof stubSchedule>) {
  await waitFor(() => {
    expect(fetchMock.mock.calls.some((c) => (c[0] as Request).method === 'PUT')).toBe(true)
  })
  const put = fetchMock.mock.calls.find((c) => (c[0] as Request).method === 'PUT')?.[0] as Request
  return (await put.json()) as {
    timezone: string
    days: { weekday: number }[]
    daily_limit: number | null
    max_new_leads_per_day: number | null
  }
}

describe('SchedulePanel', () => {
  test('renders the saved windows, timezone, and the send-time preview', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)
    await showTimeInputs()

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
    await showTimeInputs()

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /save schedule/i })).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Mon window 1 start'), { target: { value: '10:30' } })
    expect(screen.getByRole('button', { name: /save schedule/i })).toBeInTheDocument()
  })

  test('sends the edited schedule as a full replace', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)
    await showTimeInputs()

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
    await showTimeInputs()

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
    await showTimeInputs()

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
    await showTimeInputs()

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Mon window 1 end'), { target: { value: '08:00' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/end time must be after the start time/)
    expect(fetchMock.mock.calls.some((c) => (c[0] as Request).method === 'PUT')).toBe(false)
  })

  test("a rejected save surfaces the server's reason and keeps the edits", async () => {
    stubSchedule({ putStatus: 422 })
    renderWithProviders(<SchedulePanel campaignId="c-1" />)
    await showTimeInputs()

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
    await showTimeInputs()

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    expect(screen.getByLabelText('Daily limit')).toHaveValue(null)
  })

  test('a field the server omitted entirely still renders empty', async () => {
    const { daily_limit: _omitted, ...withoutLimit } = SCHEDULE
    stubSchedule({ schedule: withoutLimit })
    renderWithProviders(<SchedulePanel campaignId="c-1" />)
    await showTimeInputs()

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
    await showTimeInputs()

    await waitFor(() => expect(screen.getByText(/Mon 09:14:37/)).toBeInTheDocument())
    fireEvent.change(screen.getByLabelText('Mon window 1 start'), { target: { value: '10:30' } })

    // Showing the saved schedule's send times next to unsaved edits would claim a
    // cadence that isn't in effect.
    expect(screen.queryByText(/Mon 09:14:37/)).not.toBeInTheDocument()
    expect(screen.getByText(/Save to see the send times/)).toBeInTheDocument()
  })

  test('renders the saved new-leads-per-day limit and explains follow-ups keep flowing', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('New leads per day')).toHaveValue(25))
    expect(screen.getByText(/follow-ups keep flowing/)).toBeInTheDocument()
  })

  test('no new-leads limit shows as an empty field, not as a zero', async () => {
    stubSchedule({ schedule: { ...SCHEDULE, max_new_leads_per_day: null } })
    renderWithProviders(<SchedulePanel campaignId="c-1" />)
    await showTimeInputs()

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    expect(screen.getByLabelText('New leads per day')).toHaveValue(null)
  })

  test('a new-leads field the server omitted entirely still renders empty', async () => {
    const { max_new_leads_per_day: _omitted, ...withoutLimit } = SCHEDULE
    stubSchedule({ schedule: withoutLimit })
    renderWithProviders(<SchedulePanel campaignId="c-1" />)
    await showTimeInputs()

    await waitFor(() => expect(screen.getByLabelText('Mon window 1 start')).toBeInTheDocument())
    expect(screen.getByLabelText('New leads per day')).toHaveValue(null)
  })

  test('a typed new-leads limit is sent as a number, independent of the daily limit', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('New leads per day')).toHaveValue(25))
    fireEvent.change(screen.getByLabelText('New leads per day'), { target: { value: '10' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    const body = await readPut(fetchMock)
    expect(body.max_new_leads_per_day).toBe(10)
    expect(body.daily_limit).toBe(250)
  })

  test('clearing the new-leads limit sends null, which is how the limit is removed', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('New leads per day')).toHaveValue(25))
    fireEvent.change(screen.getByLabelText('New leads per day'), { target: { value: '' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    // Omitting the field would leave the old limit in place on a full-replace PUT.
    const body = await readPut(fetchMock)
    expect(body.max_new_leads_per_day).toBeNull()
    expect('max_new_leads_per_day' in body).toBe(true)
  })

  test('a new-leads limit below 1 is refused client-side with no request', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('New leads per day')).toHaveValue(25))
    fireEvent.change(screen.getByLabelText('New leads per day'), { target: { value: '0' } })
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/1 or more/)
    expect(fetchMock.mock.calls.some((c) => (c[0] as Request).method === 'PUT')).toBe(false)
  })

  test('editing only the new-leads limit is enough to enable the save', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    await waitFor(() => expect(screen.getByLabelText('New leads per day')).toHaveValue(25))
    expect(screen.queryByRole('button', { name: /save schedule/i })).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('New leads per day'), { target: { value: '5' } })
    expect(screen.getByRole('button', { name: /save schedule/i })).toBeInTheDocument()
  })

  // --- The schedule board ---

  test('the board is the default view, showing saved windows as cells', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    // Mon 09:00-17:00 from SCHEDULE: the 9am cell is open, 8am is not.
    expect(await screen.findByRole('button', { name: /monday 9am, open/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /monday 8am, closed/i })).toBeInTheDocument()
    // The end is exclusive, so 5pm itself is closed.
    expect(screen.getByRole('button', { name: /monday 5pm, closed/i })).toBeInTheDocument()
    // ...and the time inputs are not merely hidden, they are absent.
    expect(screen.queryByLabelText('Mon window 1 start')).not.toBeInTheDocument()
  })

  test('clicking a cell opens it and enables the save', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    fireEvent.click(await screen.findByRole('button', { name: /tuesday 10am, closed/i }))

    expect(await screen.findByRole('button', { name: /tuesday 10am, open/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save schedule/i })).toBeInTheDocument()
  })

  test('a board edit saves as merged minute intervals, not one per cell', async () => {
    const fetchMock = stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    // Open three consecutive hours on a closed day. Sequential on purpose:
    // each click must land before the next cell is queried, since the board
    // re-renders between them. Promise.all would race the three lookups
    // against one render.
    for (const hour of ['9am', '10am', '11am']) {
      // oxlint-disable-next-line no-await-in-loop -- clicks must be applied in order; see above
      fireEvent.click(await screen.findByRole('button', { name: new RegExp(`tuesday ${hour}, closed`, 'i') }))
    }
    fireEvent.click(screen.getByRole('button', { name: /save schedule/i }))

    const body = await readPut(fetchMock)
    const tuesday = body.days.find((d) => d.weekday === 2) as
      | { weekday: number; intervals: { start_minute: number; end_minute: number }[] }
      | undefined
    // Three cells become ONE interval — the run-length encoding, end to end.
    expect(tuesday?.intervals).toEqual([{ start_minute: 540, end_minute: 720 }])
  })

  test('a day header toggles the whole weekday', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)
    await screen.findByRole('button', { name: /monday 9am, open/i })

    // Monday is partly open, so the first click completes it rather than clearing.
    fireEvent.click(screen.getByRole('button', { name: 'Mon' }))

    expect(await screen.findByRole('button', { name: /monday 12am, open/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /monday 11pm, open/i })).toBeInTheDocument()
  })

  test('an entirely closed board is called out, since the API rejects it', async () => {
    stubSchedule({ schedule: { ...SCHEDULE, days: [] } })
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    expect(await screen.findByText(/nothing is open/i)).toBeInTheDocument()
  })

  // The board cannot represent minute precision, so rounding a saved 09:30
  // window into cells would silently rewrite the operator's schedule. It is
  // withheld instead, with the reason stated.
  test('a minute-precision schedule gets the time inputs and no board', async () => {
    stubSchedule({
      schedule: {
        ...SCHEDULE,
        days: [{ weekday: 1, intervals: [{ start_minute: 570, end_minute: 1035 }] }],
      },
    })
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    expect(await screen.findByText(/minute precision/i)).toBeInTheDocument()
    // The exact times are shown directly — no mode switch to find.
    expect(screen.getByLabelText('Mon window 1 start')).toHaveValue('09:30')
    expect(screen.queryByRole('button', { name: /monday 9am/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^board$/i })).not.toBeInTheDocument()
  })

  test('switching to exact times keeps a board edit', async () => {
    stubSchedule()
    renderWithProviders(<SchedulePanel campaignId="c-1" />)

    fireEvent.click(await screen.findByRole('button', { name: /tuesday 9am, closed/i }))
    fireEvent.click(screen.getByRole('button', { name: /exact times/i }))

    // Both editors read the same draft, so the new Tuesday window is there.
    expect(await screen.findByLabelText('Tue window 1 start')).toHaveValue('09:00')
  })
})

import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { TasksPanel } from '../tasks-panel'
import type { CrmTask, CrmTaskInput } from '../api'

// `PUT /crm/tasks/{id}` is a FULL REPLACE, so the fake below applies each body
// the way the API does — whatever the panel leaves out is what the task loses.
// That is deliberate: a test whose fake merges the payload would pass while the
// real thing wipes the task's title, body, due date and assignee.

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

const task = (overrides: Partial<CrmTask> = {}): CrmTask => ({
  id: 't-1',
  title: 'Send the pricing deck',
  body: 'Attach the enterprise tier and the security one-pager.',
  due_at: '2026-09-04T15:00:00.000Z',
  status: 'open',
  assignee_user_id: 'u-7',
  created_by_actor: { type: 'user' },
  created_at: '2026-08-01T00:00:00.000Z',
  updated_at: '2026-08-01T00:00:00.000Z',
  ...overrides,
})

/** The fake server's task table, mutated by the writes under test. */
let stored: CrmTask[]
let puts: { id: string; body: CrmTaskInput }[]
let deletes: string[]
let putStatus: number
let deleteStatus: number

beforeEach(() => {
  stored = [task()]
  puts = []
  deletes = []
  putStatus = 200
  deleteStatus = 204

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      // `fetchBaseQuery` calls `fetch(new Request(...))`, so the method lives on
      // the Request. Reading `init?.method` here would see every PUT and DELETE
      // as a GET and quietly assert nothing.
      const request = input instanceof Request ? input : new Request(input)
      const { pathname } = new URL(request.url)

      const single = /\/crm\/tasks\/([^/]+)$/.exec(pathname)
      const id = single?.[1]
      if (id !== undefined) {
        if (request.method === 'PUT') {
          const body = (await request.json()) as CrmTaskInput
          puts.push({ id, body })
          if (putStatus !== 200) return json({ error: 'the server fell over' }, putStatus)
          const current = stored.find((each) => each.id === id)
          if (!current) return json({ error: 'not found' }, 404)
          const replaced: CrmTask = {
            ...current,
            title: body.title,
            body: body.body,
            due_at: body.due_at ?? null,
            status: body.status,
            assignee_user_id: body.assignee_user_id ?? null,
            updated_at: '2026-08-02T00:00:00.000Z',
          }
          stored = stored.map((each) => (each.id === id ? replaced : each))
          return json(replaced)
        }
        if (request.method === 'DELETE') {
          deletes.push(id)
          if (deleteStatus !== 204) return json({ error: 'the server fell over' }, deleteStatus)
          stored = stored.filter((each) => each.id !== id)
          return new Response(null, { status: 204 })
        }
      }

      if (pathname.endsWith('/crm/tasks') && request.method === 'GET') {
        return json({ items: stored })
      }
      throw new Error(`unexpected request: ${request.method} ${pathname}`)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** The list item a task is rendered in, so assertions can't drift to another row. */
function row(title: string): HTMLElement {
  const item = screen.getByText(title).closest('li')
  if (!item) throw new Error(`no task row titled ${title}`)
  return item
}

async function completeFirstTask(): Promise<void> {
  fireEvent.click(await screen.findByRole('button', { name: /mark send the pricing deck as complete/i }))
  await waitFor(() => expect(puts).toHaveLength(1))
}

test('completing a task resends the whole task, so nothing but the status changes', async () => {
  renderWithProviders(<TasksPanel targetType="contact" targetId="c-1" />)
  await completeFirstTask()

  // The payload itself: a status-only body would be a valid request that
  // silently blanks four fields.
  expect(puts[0]?.body).toMatchObject({
    title: 'Send the pricing deck',
    body: 'Attach the enterprise tier and the security one-pager.',
    due_at: '2026-09-04T15:00:00.000Z',
    assignee_user_id: 'u-7',
    status: 'done',
  })
  // And the same fact from the server's side, after the replace was applied.
  expect(stored[0]).toMatchObject({
    title: 'Send the pricing deck',
    body: 'Attach the enterprise tier and the security one-pager.',
    due_at: '2026-09-04T15:00:00.000Z',
    assignee_user_id: 'u-7',
    status: 'done',
  })
  // The refetched row still reads as the task it was, not an empty shell.
  expect(await screen.findByText('Send the pricing deck')).toBeInTheDocument()
  expect(within(row('Send the pricing deck')).getByText(/^Due /)).toBeInTheDocument()
})

test('the update is scoped to the target the panel was given, which the task itself never carries', async () => {
  // `CrmTask` has no target_type/target_id — the list endpoint is already scoped
  // — so the only source for the required input fields is this panel's props.
  renderWithProviders(<TasksPanel targetType="company" targetId="co-9" />)
  await completeFirstTask()

  expect(puts[0]?.body.target_type).toBe('company')
  expect(puts[0]?.body.target_id).toBe('co-9')
})

test('a task completed here stays on the list, marked complete, with a way back', async () => {
  renderWithProviders(<TasksPanel targetType="contact" targetId="c-1" />)
  await completeFirstTask()

  const completed = await waitFor(() => {
    const item = row('Send the pricing deck')
    expect(within(item).getByText('Completed')).toBeInTheDocument()
    return item
  })
  // Completing is not offered twice, and the way back is right there.
  expect(within(completed).queryByRole('button', { name: /as complete/i })).not.toBeInTheDocument()
  const reopen = within(completed).getByRole('button', { name: /reopen send the pricing deck/i })

  fireEvent.click(reopen)
  await waitFor(() => expect(puts).toHaveLength(2))
  // Reopening is a full replace too — an undo that wiped the task would be worse
  // than no undo at all.
  expect(puts[1]?.body).toMatchObject({
    title: 'Send the pricing deck',
    body: 'Attach the enterprise tier and the security one-pager.',
    due_at: '2026-09-04T15:00:00.000Z',
    assignee_user_id: 'u-7',
    status: 'open',
  })
  expect(await screen.findByRole('button', { name: /mark send the pricing deck as complete/i })).toBeInTheDocument()
  expect(screen.queryByText('Completed')).not.toBeInTheDocument()
})

test('a task already done before this visit is not listed — the panel is next actions, not an archive', async () => {
  stored = [task(), task({ id: 't-2', title: 'Chase the signed order form', status: 'done' })]
  renderWithProviders(<TasksPanel targetType="contact" targetId="c-1" />)

  expect(await screen.findByText('Send the pricing deck')).toBeInTheDocument()
  expect(screen.queryByText('Chase the signed order form')).not.toBeInTheDocument()
})

test('deleting asks first, and cancelling sends nothing', async () => {
  renderWithProviders(<TasksPanel targetType="contact" targetId="c-1" />)
  fireEvent.click(await screen.findByRole('button', { name: /delete send the pricing deck/i }))

  const dialog = await screen.findByRole('alertdialog')
  expect(deletes).toEqual([])
  // The copy has to separate the two acts: one records that the work happened,
  // the other says it never should have existed.
  expect(dialog).toHaveTextContent(/cannot be undone/i)
  expect(dialog).toHaveTextContent(/mark it complete instead/i)

  fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  expect(deletes).toEqual([])
  expect(screen.getByText('Send the pricing deck')).toBeInTheDocument()
})

test('confirming the deletion removes the task', async () => {
  renderWithProviders(<TasksPanel targetType="contact" targetId="c-1" />)
  fireEvent.click(await screen.findByRole('button', { name: /delete send the pricing deck/i }))
  const dialog = await screen.findByRole('alertdialog')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Delete task' }))

  await waitFor(() => expect(deletes).toEqual(['t-1']))
  await waitFor(() => expect(screen.queryByText('Send the pricing deck')).not.toBeInTheDocument())
  expect(await screen.findByText(/no open tasks/i)).toBeInTheDocument()
})

test('a completion the server refuses is reported on the row, which stays open', async () => {
  putStatus = 500
  renderWithProviders(<TasksPanel targetType="contact" targetId="c-1" />)
  await completeFirstTask()

  const failed = row('Send the pricing deck')
  expect(await within(failed).findByRole('alert')).toHaveTextContent('The server had a problem. Try again in a moment.')
  // The task did not quietly change state, and it did not vanish.
  expect(within(failed).getByRole('button', { name: /mark send the pricing deck as complete/i })).toBeInTheDocument()
  expect(within(failed).queryByText('Completed')).not.toBeInTheDocument()
})

test('a deletion the server refuses leaves the task listed, and says why in the open', async () => {
  deleteStatus = 500
  renderWithProviders(<TasksPanel targetType="contact" targetId="c-1" />)
  fireEvent.click(await screen.findByRole('button', { name: /delete send the pricing deck/i }))
  fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete task' }))

  await waitFor(() => expect(deletes).toEqual(['t-1']))
  const failed = row('Send the pricing deck')
  expect(await within(failed).findByRole('alert')).toHaveTextContent('The server had a problem. Try again in a moment.')
  // The dialog is closed before the message renders, or the message lands
  // underneath it and nobody ever reads it.
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
})
